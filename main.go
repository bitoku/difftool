package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/fatih/color"
	"github.com/google/go-cmp/cmp"
	configv1 "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"difftool/pkg/objdiff"
	"difftool/pkg/util"
)

type Target struct {
	v1.TypeMeta `json:",inline"`
	Manifest    string   `json:"manifest"`
	Ignore      []string `json:"ignore"`
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

func getAvailableVersions(dir string) []*util.Version {
	dirEntry, _ := os.ReadDir(dir)
	var versions []*util.Version
	for _, v := range dirEntry {
		version, err := util.ParseVersion(v.Name())
		if err != nil {
			_, _ = fmt.Fprintf(os.Stdout, "warning: there is a directory whose name is not a ocp version.: %s", v.Name())
			continue
		}
		versions = append(versions, version)
	}
	// return ascending order
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Less(versions[j])
	})
	return versions
}

func fallbackPriority(version *util.Version, available []*util.Version) (out []*util.Version) {
	idx := sort.Search(len(available), func(i int) bool { return !available[i].Less(version) })
	for i := idx; i < len(available); i++ {
		out = append(out, available[i])
	}
	for i := idx - 1; i >= 0; i-- {
		out = append(out, available[i])
	}
	return out
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
	Version     *util.Version
	Fallback    bool
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
	fallback := flag.Bool("fallback", true, "fallback when the specified version is not available")
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

	var (
		parsedVersion *util.Version
		err           error
	)
	if *version != "" {
		parsedVersion, err = util.ParseVersion(*version)
		if err != nil {
			return nil, errors.Wrap(err, "couldn't parse version")
		}
	}

	return &Options{
		Kubeconfig:  *kubeconfig,
		Target:      *target,
		ManifestDir: *manifest,
		Version:     parsedVersion,
		Fallback:    *fallback,
	}, nil
}

func checkTarget(opts *Options, target *Target, version *util.Version, d objdiff.Differ) ([]string, []string, error) {
	var obj objdiff.Object

	versions := getAvailableVersions(opts.ManifestDir)

	manifest := filepath.Join(opts.ManifestDir, version.String(), target.Manifest)
	err := loadYaml(manifest, &obj)
	if err != nil && !os.IsNotExist(errors.Cause(err)) {
		return nil, nil, errors.Cause(err)
	}
	if os.IsNotExist(errors.Cause(err)) {
		// fallback if the option is set, otherwise skip the comparison
		if opts.Fallback {
			prioritized := fallbackPriority(opts.Version, versions)
			for _, v := range prioritized {
				manifest = filepath.Join(opts.ManifestDir, v.String(), target.Manifest)
				err = loadYaml(manifest, &obj)
				if err != nil && !os.IsNotExist(errors.Cause(err)) {
					return nil, nil, errors.WithStack(err)
				}
				if os.IsNotExist(errors.Cause(err)) {
					continue
				}
				fmt.Fprintf(os.Stderr, "use %s instead of %s\n", v, opts.Version)
				break
			}
		} else {
			return nil, nil, errors.WithStack(err)
		}
	}

	// check the diff
	diffOpts := []cmp.Option{objdiff.IgnoreMapEntries(target.Ignore)}
	return d.Diff(target.APIVersion, target.Kind, &obj, diffOpts...)
}

func run() error {
	// read cmd flags
	opts, err := getOpts()
	if err != nil {
		return errors.WithStack(err)
	}

	// set color
	success := color.New(color.FgGreen)
	warn := color.New(color.FgYellow)
	fail := color.New(color.FgRed)
	bold := color.New(color.Bold)

	// read targetList.yaml
	var targets []*Target
	err = loadYaml(opts.Target, &targets)
	if err != nil {
		return errors.WithStack(err)
	}

	config, err := clientcmd.BuildConfigFromFlags("", opts.Kubeconfig)
	if err != nil {
		return errors.WithStack(err)
	}

	version := opts.Version
	if opts.Version == nil {
		client, err := configv1.NewForConfig(config)
		if err != nil {
			return errors.WithStack(err)
		}
		cv, err := client.ClusterVersions().Get(context.TODO(), "version", v1.GetOptions{})
		if err != nil {
			return errors.WithStack(err)
		}
		version, err = util.ParseVersion(cv.Status.Desired.Version)
		if err != nil {
			return errors.WithStack(err)
		}
	}

	d, err := objdiff.New(config)
	if err != nil {
		return errors.WithStack(err)
	}

	for _, target := range targets {
		bold.Printf("# %s\n", filepath.Base(target.Manifest))

		presences, diffs, err := checkTarget(opts, target, version, d)
		if err != nil {
			warn.Fprintf(os.Stderr, "skipped due to error: %+v\n", err.Error())
			continue
		}

		if len(presences) == 0 && len(diffs) == 0 {
			success.Printf("No diff.\n\n")
			continue
		}
		if len(presences) != 0 {
			fail.Printf("%s\n", strings.Join(presences, ""))
		}
		if len(diffs) != 0 {
			fail.Printf("%s\n", strings.Join(diffs, "\n"))
		}
	}
	return nil
}
