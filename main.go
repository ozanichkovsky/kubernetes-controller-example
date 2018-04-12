package main

import (
	"fmt"
	"flag"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/cache"
	"k8s.io/apimachinery/pkg/fields"
	"time"
	"k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	"log"
	"github.com/ozanichkovsky/kubernetes-controller-example/slack"
	"sync"
)

func main() {

	// setup
	slackToken := flag.String("slack", "", "Slack token")
	botToken := flag.String("bot", "", "Bot Slack Token")
	k8sConfigPath := flag.String("config", "", "Path to Kubernetes config")
	flag.Parse()

	clientset, err := getClient(*k8sConfigPath)
	if err != nil {
		log.Fatalf("cannot get k8s client: %v\n", err)
	}

	ns := make(chan string)
	defer close(ns)
	messages := make(chan slack.Message)
	defer close(messages)
	wg := sync.WaitGroup{}

	slackClient, err := slack.NewSlack(*slackToken, *botToken)
	if err != nil {
		log.Fatalf("cannot login to slack: %v\n", err)
	}
	slackClient.SetChannelChan(ns)
	slackClient.SetMessageChan(messages)

	wg.Add(1)
	go func() {
		defer wg.Done()
		watchNamespaces(clientset, ns)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		watchDeployments(clientset, messages)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		slackClient.Proceed()
	}()
	wg.Wait()
}

func getClient(pathToCfg string) (*kubernetes.Clientset, error) {
	var config *rest.Config
	var err error
	if pathToCfg == "" {
		fmt.Println("Using in cluster config")
		config, err = rest.InClusterConfig()
		// in cluster access
	} else {
		fmt.Println("Using out of cluster config")
		config, err = clientcmd.BuildConfigFromFlags("", pathToCfg)
	}
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func watchNamespaces(clientset *kubernetes.Clientset, namespaces chan<- string) {
	watchList := cache.NewListWatchFromClient(clientset.CoreV1().RESTClient(), "namespaces", v1.NamespaceAll,
		fields.Everything())

	addFunc := func(obj interface{}) {
		namespace := obj.(*v1.Namespace)
		namespaces <- namespace.Name
	}

	_, controller := cache.NewInformer(
		watchList,
		&v1.Node{},
		time.Second*30,
		cache.ResourceEventHandlerFuncs{
			AddFunc: addFunc,
		},
	)

	stop := make(chan struct{})
	go controller.Run(stop)
}

func watchDeployments(clientset *kubernetes.Clientset, messages chan<- slack.Message) {
	watchList := cache.NewListWatchFromClient(clientset.AppsV1().RESTClient(), "deployments", v1.NamespaceAll,
		fields.Everything())

	addFunc := func(obj interface{}) {
		deployment := obj.(*appsv1.Deployment)
		messages <- slack.Message{
			Channel: deployment.Namespace,
			Message: fmt.Sprintf("Deployment %s with scale %d created", deployment.Name, deployment.Status.Replicas),
		}
	}

	updateFunc := func(oldObj interface{}, newObj interface{}) {
		oldDeployment := oldObj.(*appsv1.Deployment)
		newDeployment := newObj.(*appsv1.Deployment)
		if oldDeployment.Status.Replicas == newDeployment.Status.Replicas {
			return
		}

		messages <- slack.Message{
			Channel: newDeployment.Namespace,
			Message: fmt.Sprintf("Deployment %s scaled from %d to %d", newDeployment.Name, oldDeployment.Status.Replicas, newDeployment.Status.Replicas),
		}
	}

	_, controller := cache.NewInformer(
		watchList,
		&appsv1.Deployment{},
		time.Second*30,
		cache.ResourceEventHandlerFuncs{
			AddFunc: addFunc,
			UpdateFunc: updateFunc,
		},
	)

	stop := make(chan struct{})
	go controller.Run(stop)
}