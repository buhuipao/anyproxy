#!/bin/bash

# Generate self-signed certificate with support for multiple domains and IPs
# Usage: ./generate_certs.sh <primary_domain> [additional_domains_or_ips...]
# Example: ./generate_certs.sh gateway-sz-01.tp.buhuipao.com 47.107.181.88 *.tp.buhuipao.com

# Get the primary domain from the first argument
PRIMARY=$1
if [ -z "$PRIMARY" ]; then
    PRIMARY="localhost"
fi

# Create certs directory if it doesn't exist
mkdir -p certs

# Build SAN (Subject Alternative Names) from all arguments
SAN=""
for arg in "$@"; do
    if [ -n "$SAN" ]; then
        SAN="$SAN,"
    fi
    # Check if argument is an IP address
    if [[ $arg =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        SAN="${SAN}IP:$arg"
    else
        SAN="${SAN}DNS:$arg"
    fi
done

# Add localhost and 127.0.0.1 as defaults if not already included
if [[ ! "$SAN" =~ "DNS:localhost" ]]; then
    SAN="${SAN},DNS:localhost"
fi
if [[ ! "$SAN" =~ "IP:127.0.0.1" ]]; then
    SAN="${SAN},IP:127.0.0.1"
fi

# Handle empty SAN (when no arguments provided)
if [ -z "$SAN" ] || [ "$SAN" == "," ]; then
    SAN="DNS:localhost,IP:127.0.0.1"
fi

# Remove leading comma if exists
SAN="${SAN#,}"

echo "Generating certificate..."
echo "Primary CN: $PRIMARY"
echo "Subject Alternative Names: $SAN"

# Generate self-signed certificate (valid for 10 years = 3650 days)
openssl req -x509 -newkey rsa:4096 -keyout certs/server.key -out certs/server.crt -days 3650 -nodes -subj "/CN=$PRIMARY" -addext "subjectAltName = $SAN"

echo ""
echo "Certificate generated successfully!"
echo "Certificate location: certs/server.crt"
echo "Key location: certs/server.key"
echo "Certificate is valid for 10 years"
echo "Certificate covers: $SAN" 