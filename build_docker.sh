#!/bin/bash

if [ $# != 1 ]; then
    echo "e.g.: bash $0 v1.0"
    exit 1
fi

docker_repository="polarismesh/polaris-limiter"
docker_tag=$1

echo "docker repository : ${docker_repository}, tag : ${docker_tag}"

bash build.sh ${docker_tag}

if [ $? != 0 ]; then
    echo "build polaris-limiter failed"
    exit 1
fi

docker build --network=host -t ${docker_repository}:${docker_tag} ./

docker push ${docker_repository}:${docker_tag}
