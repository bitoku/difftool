package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func normalize(obj any) any {
	switch x := obj.(type) {
	case map[string]any:
		result := make(map[string]any)
		for k, v := range x {
			result[k] = normalize(v)
		}
		return result
	case []any:
		var result []any
		for _, v := range x {
			result = append(result, normalize(v))
		}
		return result
	case int:
		return int64(x)
	}
	return obj
}

type Metadata struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

type Target struct {
	ApiVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   `yaml:"metadata"`
	Manifest   string `yaml:"manifest"`
	Resource   schema.GroupVersionResource
}

func getResource(apiVersion, kind string, mapper meta.RESTMapper) (schema.GroupVersionResource, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}

	gvk := gv.WithKind(kind)
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	return mapping.Resource, nil
}

func check(targets []Target, client dynamic.Interface) ([]string, error) {
	var result []string
	for _, target := range targets {
		// get the current manifest
		resp, err := client.
			Resource(target.Resource).
			List(context.Background(), v1.ListOptions{})
		if err != nil {
			return result, err
		}

		// get the default manifest
		content, err := os.ReadFile(target.Manifest)
		if err != nil {
			return result, err
		}

		var manifest map[string]any
		err = yaml.Unmarshal(content, &manifest)
		if err != nil {
			return result, err
		}

		normalized := normalize(manifest).(map[string]any)
		for _, item := range resp.Items {
			diff := cmp.Diff(normalized["spec"], item.Object["spec"])
			result = append(result, diff)
		}
	}
	return result, nil
}

func main() {
	// read cmd flags
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	targetList := flag.String("target", "", "path to the yaml file of target properties")
	flag.Parse()
	// read settings yaml
	if *targetList == "" {
		panic("--target option is required")
	}
	targetListPath, err := filepath.Abs(*targetList)
	var targets []Target
	f, err := os.ReadFile(targetListPath)
	if err != nil {
		panic(err.Error())
	}
	err = yaml.Unmarshal(f, &targets)
	if err != nil {
		panic(err.Error())
	}

	// create a mapper to get a gvr
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	groupResources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		panic(err.Error())
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

	// normalize the manifest paths to the absolute paths
	for i, target := range targets {
		if !filepath.IsAbs(target.Manifest) {
			targets[i].Manifest = filepath.Join(filepath.Dir(targetListPath), target.Manifest)
		}
		targets[i].Resource, err = getResource(target.ApiVersion, target.Kind, mapper)
		if err != nil {
			panic(err.Error())
		}
	}

	// create the client
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	diffs, err := check(targets, client)
	if err != nil {
		panic(err.Error())
	}
	for _, diff := range diffs {
		fmt.Println(diff)
	}
}
