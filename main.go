package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/go-cmp/cmp"
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

	"difftool/pkg/objdiff"
)

type Target struct {
	v1.TypeMeta `json:",inline"`
	Manifest    string   `json:"manifest"`
	Ignore      []string `json:"ignore"`
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

// if we unmarshall yaml directly, int64 is inferred as float64 somehow,
// so we convert yaml to json first and then unmarshall it
func unmarshall(data []byte, v any) error {
	jsonContent, err := yaml.ToJSON(data)
	if err != nil {
		return err
	}
	err = json.Unmarshal(jsonContent, v)
	return err
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
		var obj objdiff.Object
		fmt.Println(filepath.Base(target.Manifest))
		content, err := os.ReadFile(target.Manifest)
		if err != nil {
			panic(err.Error())
		}

		err = unmarshall(content, &obj)
		if err != nil {
			panic(err.Error())
		}

		resource, err := getResource(target.APIVersion, target.Kind, mapper)
		if err != nil {
			panic(err.Error())
		}

		//check the diff
		var diff string
		opts := []cmp.Option{objdiff.IgnoreMapEntries(target.Ignore)}
		if err != nil {
			panic(err.Error())
		}
		if obj.IsList() {
			diff, err = objdiff.CheckList(&obj, client, resource, opts...)
		} else {
			diff, err = objdiff.CheckObj(&obj, client, resource, opts...)
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
