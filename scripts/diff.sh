for file in default/*; do
  echo $file
  go run hack/compare.go default/4.11.26/dns.yaml $file/dns.yaml
done