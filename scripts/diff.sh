for file in default/4.12.*; do
  echo $file
  go run hack/compare.go default/4.12.25/kubeletconfig.yaml $file/kubeletconfig.yaml
done