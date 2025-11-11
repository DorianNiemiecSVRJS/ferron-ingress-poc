package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type ConfigGenerator struct {
	mu            sync.Mutex
	client        *kubernetes.Clientset
	ingressClass  string
	outputPath    string
	ferronPath    string
	ferronProcess *os.Process
}

func NewConfigGenerator(client *kubernetes.Clientset, class, output, ferronPath string) *ConfigGenerator {
	return &ConfigGenerator{
		client:        client,
		ingressClass:  class,
		outputPath:    output,
		ferronPath:    ferronPath,
		ferronProcess: nil,
	}
}

func (cg *ConfigGenerator) shouldHandle(ing *networkingv1.Ingress) bool {
	if ing.Spec.IngressClassName == nil {
		return false
	}
	return *ing.Spec.IngressClassName == cg.ingressClass
}

func (cg *ConfigGenerator) RebuildConfig() {
	cg.mu.Lock()
	defer cg.mu.Unlock()

	ingresses, err := cg.client.NetworkingV1().Ingresses("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Printf("Error listing ingresses: %v", err)
		return
	}

	// Setting some configuration property to a default value,
	// just to make Ferron listen on port 80 when no endpoint is found
	var conf = ":80 {\n  trust_x_forwarded_for #false\n}\n"

	for _, ing := range ingresses.Items {
		if !cg.shouldHandle(&ing) {
			continue
		}

		for _, rule := range ing.Spec.Rules {
			host := rule.Host
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				svcName := path.Backend.Service.Name

				endpointSlices, err := cg.client.DiscoveryV1().EndpointSlices(ing.Namespace).List(context.Background(),
					metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName + "=" + svcName})
				if err != nil {
					fmt.Printf("failed to get endpoints for %s/%s: %v\n", ing.Namespace, svcName, err)
					continue
				}

				conf += fmt.Sprintf("\"%s:80\" {\n  location \"%s\" remove_base=#true {\n", host, path.Path)
				for _, endpointSlice := range endpointSlices.Items {
					for _, endpoint := range endpointSlice.Endpoints {
						portPtr := endpointSlice.Ports[0].Port
						port := int32(80)
						if portPtr != nil {
							port = *portPtr
						}
						for _, addr := range endpoint.Addresses {
							conf += fmt.Sprintf("    proxy \"http://%s:%d/\"\n", addr, port)
						}
					}
				}
				conf += "  }\n}\n"
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(cg.outputPath), 0755); err != nil {
		log.Printf("Error creating directory: %v", err)
		return
	}

	if err := os.WriteFile(cg.outputPath, []byte(conf), 0644); err != nil {
		log.Printf("Error writing config file: %v", err)
		return
	}

	log.Printf("Updated config file at %s", cg.outputPath)

	killedSuccessfully := false
	if cg.ferronProcess != nil {
		if err := cg.ferronProcess.Signal(syscall.SIGHUP); err == nil {
			killedSuccessfully = true
		}
	}

	if !killedSuccessfully {
		attr := &os.ProcAttr{
			Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		}
		ferronProcess, err := os.StartProcess(cg.ferronPath, []string{cg.ferronPath, "-c", cg.outputPath}, attr)
		if err != nil {
			log.Printf("Error starting Ferron process: %v", err)
		} else if err := ferronProcess.Signal(syscall.Signal(0)); err != nil {
			log.Printf("Error checking Ferron process status: %v", err)
		} else {
			log.Printf("Ferron process started successfully.")
		}
		cg.ferronProcess = ferronProcess
	} else {
		log.Printf("Ferron configuration reloaded successfully.")
	}
}

func main() {
	var ingressClass, configPath, ferronPath string
	flag.StringVar(&ingressClass, "ingress-class", "ferron-poc", "IngressClass name to handle")
	flag.StringVar(&configPath, "config-path", "/etc/ferron.kdl", "Path to generated config file")
	flag.StringVar(&ferronPath, "ferron-path", "/usr/bin/ferron", "Path to Ferron binary")
	flag.Parse()

	clientset, err := buildClientset()
	if err != nil {
		log.Fatalf("Error creating clientset: %v", err)
	}

	generator := NewConfigGenerator(clientset, ingressClass, configPath, ferronPath)

	// Build the Ferron configuration for the first time
	generator.RebuildConfig()

	factory := informers.NewSharedInformerFactory(clientset, 30*time.Second)
	ingressInformer := factory.Networking().V1().Ingresses().Informer()

	ingressInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			ing := obj.(*networkingv1.Ingress)
			if generator.shouldHandle(ing) {
				log.Printf("[ADD] %s/%s", ing.Namespace, ing.Name)
				generator.RebuildConfig()
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			newIng := newObj.(*networkingv1.Ingress)
			if generator.shouldHandle(newIng) {
				log.Printf("[UPDATE] %s/%s", newIng.Namespace, newIng.Name)
				generator.RebuildConfig()
			}
		},
		DeleteFunc: func(obj any) {
			ing := obj.(*networkingv1.Ingress)
			if generator.shouldHandle(ing) {
				log.Printf("[DELETE] %s/%s", ing.Namespace, ing.Name)
				generator.RebuildConfig()
			}
		},
	})

	stopCh := make(chan struct{})
	defer close(stopCh)

	log.Printf("Starting config-based ingress controller for class=%q", ingressClass)
	factory.Start(stopCh)

	if !cache.WaitForCacheSync(context.Background().Done(), ingressInformer.HasSynced) {
		runtime.HandleError(fmt.Errorf("timed out waiting for cache sync"))
		return
	}

	<-stopCh
}

func buildClientset() (*kubernetes.Clientset, error) {
	var config *rest.Config
	var err error

	config, err = rest.InClusterConfig()
	if err != nil {
		if home := homedir.HomeDir(); home != "" {
			kubeconfig := fmt.Sprintf("%s/.kube/config", home)
			config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
			if err != nil {
				return nil, err
			}
		}
	}

	return kubernetes.NewForConfig(config)
}
