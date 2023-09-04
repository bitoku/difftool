package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type Object struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata"`
	Spec          map[string]any `json:"spec"`
}

func (o *Object) String() string {
	if o.Namespace == "" {
		return fmt.Sprintf("%s %s %s", o.APIVersion, o.Kind, o.Name)
	}
	return fmt.Sprintf("%s %s %s/%s", o.APIVersion, o.Kind, o.Namespace, o.Name)

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

func check(obj *Object, client dynamic.Interface, resource schema.GroupVersionResource) (string, error) {
	// get the current manifest
	resp, err := client.
		Resource(resource).
		Get(context.Background(), obj.Name, v1.GetOptions{})
	if errors.IsNotFound(err) {
		return fmt.Sprintf("%s not found", obj), nil
	}
	if err != nil {
		return "", err
	}
	rawJson, err := resp.MarshalJSON()
	if err != nil {
		return "", err
	}
	fmt.Println(string(rawJson))
	var curr Object
	json.Unmarshal(rawJson, &curr)
	return cmp.Diff(obj.Spec, curr.Spec), nil
}

func main() {
	// read cmd flags
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	dir := flag.String("dir", "", "path to the yaml file of target properties")
	flag.Parse()

	// read settings yaml
	if *dir == "" {
		panic("--target option is required")
	}
	files, err := os.ReadDir(*dir)
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

	// create the client
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	var diffs []string
	for _, f := range files {
		var obj Object
		content, err := os.ReadFile(filepath.Join(*dir, f.Name()))
		if err != nil {
			panic(err.Error())
		}
		// if we unmarshall directly from yaml, int64 is inferred as float64 somehow
		// so we convert yaml to json first and then unmarshall it
		jsonContent, err := yaml.ToJSON(content)
		if err != nil {
			panic(err.Error())
		}
		err = json.Unmarshal(jsonContent, &obj)
		if err != nil {
			panic(err.Error())
		}
		// get gvr
		resource, err := getResource(obj.APIVersion, obj.Kind, mapper)
		if err != nil {
			panic(err.Error())
		}
		//check the diff
		diff, err := check(&obj, client, resource)
		if err != nil {
			panic(err.Error())
		}
		diffs = append(diffs, diff)
	}

	for _, diff := range diffs {
		fmt.Println(diff)
	}
}
