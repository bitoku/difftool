# difftool

## How to run

```bash
# in the project directory
go install
difftool --target targetList.yaml --manifest default
```

or

```bash
go run main.go --target targetList.yaml --manifest default
```

## Options

```
  -cluster-version string
        cluster version. auto detect by default
  -fallback
        fallback when the specified version is not available (default true)
  -kubeconfig string
        absolute path to the kubeconfig file (default "/Users/***/.kube/config")
  -manifest string
        path to the directory of default manifests
  -target string
        path to the target list yaml
```