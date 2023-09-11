package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
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

func loadYaml(path string, v any) error {
	file, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return unmarshall(file, v)
}

func main() {
	err := run()
	if err != nil {
		panic(err.Error())
	}
}

func run() error {
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
		return fmt.Errorf("--target option is required")
	}
	targetAbsPath, err := filepath.Abs(*targetPath)
	if err != nil {
		return err
	}

	var targets []*Target
	err = loadYaml(*targetPath, &targets)
	if err != nil {
		return fmt.Errorf("unable to load target yaml: %s", err.Error())
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
		return err
	}

	mapper, err := objdiff.GetRESTMapper(config)
	if err != nil {
		return fmt.Errorf("failed to get RESTMapper: %s", err.Error())
	}

	// create the client
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	d := objdiff.New(client)
	for _, target := range targets {
		var obj objdiff.Object
		fmt.Printf("# %s\n", filepath.Base(target.Manifest))

		err = loadYaml(target.Manifest, &obj)
		if err != nil {
			return fmt.Errorf("failed to load object: %s", err.Error())
		}

		resource, err := getResource(target.APIVersion, target.Kind, mapper)
		if err != nil {
			return err
		}

		//check the diff
		opts := []cmp.Option{objdiff.IgnoreMapEntries(target.Ignore)}

		presences, diffs, err := d.Diff(resource, &obj, opts...)
		if err != nil {
			return err
		}

		if len(presences) == 0 && len(diffs) == 0 {
			fmt.Printf("No diff.\n\n")
			continue
		}
		if len(presences) != 0 {
			fmt.Printf("%s\n", strings.Join(presences, ""))
		}
		if len(diffs) != 0 {
			fmt.Printf("%s\n", strings.Join(diffs, "\n"))
		}
	}
	return nil
}
