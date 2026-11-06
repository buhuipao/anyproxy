#!/bin/bash

set -e

echo "Building Docker image..."
make docker-build

echo "Saving Docker image to tar file..."
docker save -o anyproxy.tar anyproxy:latest

echo "Uploading tar file to Aliyun server..."
scp anyproxy.tar aliyun-root:/root/anyproxy/

echo "Loading Docker image on remote server..."
ssh aliyun-root "docker load -i /root/anyproxy/anyproxy.tar"

echo "Restarting gateway service..."
ssh aliyun-root "cd /root/anyproxy/ && bash ./restart.sh"

echo "Cleaning up local tar file..."
rm anyproxy.tar

echo "Deployment completed successfully!"
