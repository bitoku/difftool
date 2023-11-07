package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cockroachdb/errors"
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
		return schema.GroupVersionResource{}, errors.WithStack(err)
	}

	gvk := gv.WithKind(kind)
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, errors.WithStack(err)
	}
	return mapping.Resource, nil
}

// if we unmarshall yaml directly, int64 is inferred as float64 somehow,
// so we convert yaml to json first and then unmarshall it
func unmarshall(data []byte, v any) error {
	jsonContent, err := yaml.ToJSON(data)
	if err != nil {
		return errors.WithStack(err)
	}
	err = json.Unmarshal(jsonContent, v)
	return errors.WithStack(err)
}

func loadYaml(path string, v any) error {
	file, err := os.ReadFile(path)
	if err != nil {
		return errors.WithStack(err)
	}
	return unmarshall(file, v)
}

func main() {
	err := run()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%+v", err)
		if err != nil {
			return
		}
		panic(err.Error())
	}
}

type Options struct {
	Kubeconfig  string
	Target      string
	ManifestDir string
	Version     string
}

func getOpts() (*Options, error) {
	var kubeconfigDefault string
	if home := homedir.HomeDir(); home != "" {
		kubeconfigDefault = filepath.Join(home, ".kube", "config")
	}
	kubeconfig := flag.String("kubeconfig", kubeconfigDefault, "absolute path to the kubeconfig file")
	target := flag.String("target", "", "path to the target list yaml")
	manifest := flag.String("manifest", "", "path to the directory of default manifests")
	version := flag.String("cluster-version", "", "cluster version")
	flag.Parse()

	// validate options
	if *kubeconfig == "" {
		return nil, fmt.Errorf("--kubeconfig option is required")
	}
	if *target == "" {
		return nil, fmt.Errorf("--target option is required")
	}
	if *manifest == "" {
		return nil, fmt.Errorf("--manifest option is required")
	}
	if *version == "" {
		return nil, fmt.Errorf("--cluster-version option is required")
	}
	r := regexp.MustCompile(`4\.\d+\.\d+`)
	if !r.MatchString(*version) {
		return nil, fmt.Errorf("version must be in the form of 4.y.z")
	}

	return &Options{
		Kubeconfig:  *kubeconfig,
		Target:      *target,
		ManifestDir: *manifest,
		Version:     *version,
	}, nil
}

func run() error {
	// read cmd flags
	opts, err := getOpts()
	if err != nil {
		return errors.WithStack(err)
	}

	// read targetList.yaml
	var targets []*Target
	err = loadYaml(opts.Target, &targets)
	if err != nil {
		return errors.WithStack(err)
	}

	// normalize manifest paths
	for _, target := range targets {
		if filepath.IsAbs(target.Manifest) {
			continue
		}
		target.Manifest = filepath.Join(opts.ManifestDir, opts.Version, target.Manifest)
	}

	// create a mapper to get a gvr
	config, err := clientcmd.BuildConfigFromFlags("", opts.Kubeconfig)
	if err != nil {
		return errors.WithStack(err)
	}

	mapper, err := objdiff.GetRESTMapper(config)
	if err != nil {
		return errors.WithStack(err)
	}

	// create the client
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return errors.WithStack(err)
	}

	d := objdiff.New(client)
	for _, target := range targets {
		var obj objdiff.Object
		fmt.Printf("# %s\n", filepath.Base(target.Manifest))

		err = loadYaml(target.Manifest, &obj)
		if err != nil {
			return errors.WithStack(err)
		}

		resource, err := getResource(target.APIVersion, target.Kind, mapper)
		if err != nil {
			return errors.WithStack(err)
		}

		// check the diff
		diffOpts := []cmp.Option{objdiff.IgnoreMapEntries(target.Ignore)}
		presences, diffs, err := d.Diff(resource, &obj, diffOpts...)
		if err != nil {
			return errors.WithStack(err)
		}

		// format output
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
