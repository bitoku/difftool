package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	"k8s.io/utils/strings/slices"
)

type Target struct {
	v1.TypeMeta `json:",inline"`
	Manifest    string   `json:"manifest"`
	Ignore      []string `json:"ignore"`
}

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

func ignoreMapEntries(ignoredKeys []string) cmp.Option {
	filter := func(path cmp.Path) bool {
		var key []string
		for _, ps := range path {
			switch x := ps.(type) {
			case cmp.MapIndex:
				key = append(key, x.Key().String())
			case cmp.SliceIndex:
				key = append(key, strconv.Itoa(x.Key()))
			}
		}
		// check it naively since ignoredKeys won't be so long,
		return slices.Contains(ignoredKeys, strings.Join(key, "."))
	}
	return cmp.FilterPath(filter, cmp.Ignore())
}

func compare(a, b *Object, opts ...cmp.Option) string {
	return cmp.Diff(a.Spec, b.Spec, opts...)
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

func checkObj(obj *Object, client dynamic.Interface, resource schema.GroupVersionResource, opts ...cmp.Option) (string, error) {
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

	curr := new(Object)
	err = UnmarshallUnstructured(resp, curr)
	if err != nil {
		return "", err
	}

	return compare(obj, curr, opts...), nil
}

func checkList(obj *Object, client dynamic.Interface, resource schema.GroupVersionResource, opts ...cmp.Option) (string, error) {
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
		diff := compare(i, curr, opts...)
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

func main() {
	// read cmd flags
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	targetPath := flag.String("target", "", "path to the yaml file of target properties")
	flag.Parse()

	// read settings yaml
	if *targetPath == "" {
		panic("--target option is required")
	}
	targetAbsPath, err := filepath.Abs(*targetPath)
	if err != nil {
		panic(err.Error())
	}
	file, err := os.ReadFile(targetAbsPath)
	if err != nil {
		panic(err.Error())
	}

	var targets []*Target
	err = yaml.Unmarshal(file, &targets)
	if err != nil {
		panic(err.Error())
	}

	// make manifest path absolute path
	for _, target := range targets {
		if filepath.IsAbs(target.Manifest) {
			continue
		}
		target.Manifest = filepath.Join(filepath.Dir(targetAbsPath), target.Manifest)
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

	for _, target := range targets {
		var obj Object
		fmt.Println(filepath.Base(target.Manifest))
		content, err := os.ReadFile(target.Manifest)
		if err != nil {
			panic(err.Error())
		}
		resource, err := getResource(target.APIVersion, target.Kind, mapper)
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
		opts := []cmp.Option{ignoreMapEntries(target.Ignore)}
		if obj.APIVersion == "v1" && obj.Kind == "List" {
			diff, err = checkList(&obj, client, resource, opts...)
		} else {
			diff, err = checkObj(&obj, client, resource, opts...)
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
