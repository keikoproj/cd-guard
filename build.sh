#!/usr/bin/env bash

package="cmd/cd-guard.go"

if [[ -z "$package" ]]; then
  echo "usage: $0 <package-name>"
  exit 1
fi
package_name="cd-guard"

# perform static compilation
# ldflags="-X"

platforms=("linux/amd64" "linux/386" "darwin/amd64")


GO111MODULE=on

for platform in "${platforms[@]}"
do
    platform_split=(${platform//\// })
    CGO_ENABLED=0
    GOOS=${platform_split[0]}
    GOARCH=${platform_split[1]}
    output_name=$package_name'-'$GOOS'-'$GOARCH
    if [ $GOOS = "windows" ]; then
        output_name+='.exe'
    fi  

    output_path='./release/'$output_name
    echo $output_path

    env CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -v -i -o $output_path $package
    if [ $? -ne 0 ]; then
        echo 'An error has occurred! Aborting the script execution...'
        exit 1
    fi
    cd release
    tar -zcvf $output_name.tar.gz $output_name
    rm -f $output_name
    cd ..
done
