package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	Spec          map[string]any `json:"spec,omitempty"`
	Items         []*Object      `json:"items,omitempty"`
}

func (o *Object) String() string {
	if o.Namespace == "" {
		return fmt.Sprintf("%s %s %s", o.APIVersion, o.Kind, o.Name)
	}
	return fmt.Sprintf("%s %s %s/%s", o.APIVersion, o.Kind, o.Namespace, o.Name)
}

func UnmarshallUnstructured(u *unstructured.Unstructured, v any) error {
	rawJson, err := u.MarshalJSON()
	if err != nil {
		return err
	}
	return json.Unmarshal(rawJson, v)
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

func checkObj(obj *Object, client dynamic.Interface, resource schema.GroupVersionResource) (string, error) {
	// get the current manifest
	resp, err := client.
		Resource(resource).
		Get(context.Background(), obj.Name, v1.GetOptions{})
	if errors.IsNotFound(err) {
		return fmt.Sprintf("- %s is not found\n", obj), nil
	}
	if err != nil {
		return "", err
	}

	var curr Object
	err = UnmarshallUnstructured(resp, &curr)
	if err != nil {
		return "", err
	}

	return cmp.Diff(obj.Spec, curr.Spec), nil
}

func checkList(obj *Object, client dynamic.Interface, resource schema.GroupVersionResource) (string, error) {
	resp, err := client.
		Resource(resource).
		List(context.Background(), v1.ListOptions{})
	if err != nil {
		return "", err
	}

	m := make(map[string]*Object)
	checked := make(map[string]bool)
	for _, i := range resp.Items {
		curr := new(Object)
		err = UnmarshallUnstructured(&i, curr)
		if err != nil {
			return "", err
		}
		m[curr.String()] = curr
		checked[curr.String()] = false
	}
	var presence []string
	var diffs []string
	for _, i := range obj.Items {
		curr, ok := m[i.String()]
		if !ok {
			presence = append(presence, fmt.Sprintf("- %s is not found\n", i))
			continue
		}
		checked[curr.String()] = true
		diff := cmp.Diff(i.Spec, curr.Spec)
		if diff == "" {
			continue
		}
		diffs = append(diffs, fmt.Sprintf("%s\n%s", i.String(), diff))
	}
	for k, v := range m {
		if !checked[k] {
			presence = append(presence, fmt.Sprintf("+ %s is found, but not in default\n", v))
		}
	}
	diffs = append(presence, diffs...)
	return strings.Join(diffs, "\n"), nil
}

func validateList(obj *Object) bool {
	if len(obj.Items) == 0 {
		return true
	}
	o := obj.Items[0]
	for _, v := range obj.Items {
		if v.APIVersion != o.APIVersion || v.Kind != o.Kind {
			return false
		}
	}
	return true
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

	for _, f := range files {
		var obj Object
		content, err := os.ReadFile(filepath.Join(*dir, f.Name()))
		fmt.Println(f.Name())
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
		//check the diff
		var diff string
		if obj.APIVersion == "v1" && obj.Kind == "List" {
			if !validateList(&obj) {
				panic(fmt.Sprintf("%s contains different apiVersion or kind\n", f.Name()))
			}
			o := obj.Items[0]
			resource, err := getResource(o.APIVersion, o.Kind, mapper)
			if err != nil {
				panic(err.Error())
			}
			diff, err = checkList(&obj, client, resource)
		} else {
			resource, err := getResource(obj.APIVersion, obj.Kind, mapper)
			// get gvr
			if err != nil {
				panic(err.Error())
			}
			diff, err = checkObj(&obj, client, resource)
		}
		if err != nil {
			panic(err.Error())
		}
		if diff == "" {
			fmt.Println("No diff\n")
		} else {
			fmt.Println(diff)
		}
	}
}
