#!/bin/bash


# Get the domain from the first argument
DOMAIN=$1
if [ -z "$DOMAIN" ]; then
    DOMAIN="localhost"
fi

# Create certs directory if it doesn't exist
mkdir -p certs

# Check if DOMAIN is an IP address
if [[ $DOMAIN =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    # If it's an IP address, use it in the SAN
    SAN="IP:$DOMAIN,IP:127.0.0.1"
else
    # If it's a domain name, use it in the SAN
    SAN="DNS:$DOMAIN,DNS:localhost,IP:127.0.0.1"
fi

# Generate self-signed certificate
openssl req -x509 -newkey rsa:4096 -keyout certs/server.key -out certs/server.crt -days 365 -nodes -subj "/CN=$DOMAIN" -addext "subjectAltName = $SAN"

echo "Certificate generated successfully!"
echo "Certificate location: certs/server.crt"
echo "Key location: certs/server.key"
echo "Certificate is valid for: $SAN" 