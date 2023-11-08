for file in **/*; do
  base=$(basename $file)
  dir=$(dirname $file)
  if [ $base == "mcp.yaml" ]; then
    mv "$file" "$dir/machineconfigpool.yaml"
  fi
done