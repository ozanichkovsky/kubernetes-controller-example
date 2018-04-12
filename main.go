package main

import (
	"fmt"
	"flag"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/cache"
	"k8s.io/apimachinery/pkg/fields"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
	"k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	"log"
	"github.com/ozanichkovsky/kubernetes-controller-example/slack"
	"sync"
	"strings"
	"strconv"
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
	incoming := slackClient.GetIncomingMessagesChan()

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

	wg.Add(1)
	go func() {
		defer wg.Done()
		scaleDeployments(clientset, incoming, messages)
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
			Message: fmt.Sprintf("Deployment `%s` with scale `%d` created", deployment.Name, deployment.Status.Replicas),
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
			Message: fmt.Sprintf("Deployment `%s` scaled from `%d` to `%d`", newDeployment.Name, oldDeployment.Status.Replicas, newDeployment.Status.Replicas),
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

func scaleDeployments(clientset *kubernetes.Clientset, incoming <-chan slack.Message, outgoing chan<- slack.Message) {
	for m := range incoming {
		parts := strings.Split(m.Message, " ")
		formatError := "Use `@testbot scale deployment-name 4` format"
		if len(parts) < 4 || len(parts) > 4 {
			outgoing <- slack.Message{
				ChannelId: m.ChannelId,
				Message:   formatError,
			}
		}
		if parts[1] != "scale" {
			outgoing <- slack.Message{
				ChannelId: m.ChannelId,
				Message:   formatError,
			}
		}
		replicas, err := strconv.Atoi(parts[3])
		replicasInt32 := int32(replicas)
		if err != nil {
			outgoing <- slack.Message{
				ChannelId: m.ChannelId,
				Message:   formatError,
			}
		}

		deploymentName := parts[2]
		deploymentsClient := clientset.AppsV1().Deployments(m.Channel)
		deployment, err := deploymentsClient.Get(deploymentName, metav1.GetOptions{})

		if err != nil {
			outgoing <- slack.Message{
				ChannelId: m.ChannelId,
				Message:   fmt.Sprintf("Couldn't get deployment `%s`: %v", deploymentName, err),
			}
		}

		deployment.Spec.Replicas = &replicasInt32

		_, updateErr := deploymentsClient.Update(deployment)
		if updateErr != nil {
			outgoing <- slack.Message{
				ChannelId: m.ChannelId,
				Message:   fmt.Sprintf("Was not able to update deployment `%s`: %v", deploymentName, updateErr),
			}
		}

		outgoing <- slack.Message{
			ChannelId: m.ChannelId,
			Message:   fmt.Sprintf("Scaling deployment `%s` to `%d` replicas...", deploymentName, replicas),
		}
	}
}