# AnyProxy Demo - Quick Start

The simplest way to experience AnyProxy client using Docker.

## 🚀 One-Command Start

⚠️ **Important Security**: Change `group_id` in config before running!

```bash
# 1. ⚠️ Remember: Use your unique group_id for security!
nano configs/client.yaml
# Change: group_id: "changed-to-your-group-id" 
# To: group_id: "my-unique-group-123"

# 2. For remote gateway, use the demo certs
ls -al certs/

# 3. Start client with certificate mount
cd demo && docker run -d \
  --name anyproxy-demo-client \
  --network host \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  buhuipao/anyproxy:latest \
  ./anyproxy-client --config configs/client.yaml
```

## ✅ Verify Running

```bash
# Check if client connected successfully
docker logs anyproxy-demo-client

# Access web interface: http://localhost:8091
# Login: admin / admin123
```

## 📊 What's Included

- **Pre-configured client** connects to demo gateway `47.107.181.88:8443`
- **Web interface** at http://localhost:8091 (admin / admin123)
- **Security**: ⚠️ **Must change `group_id` - default has security risks!**

## 🧪 Test Connection

```bash
# Test with your group_id (replace "my-unique-group-123" with yours)
curl -x http://user.my-unique-group-123:password@47.107.181.88:8080 http://httpbin.org/ip
```

## 🔧 Clean Up

```bash
# Stop and remove when done
docker stop anyproxy-demo-client
docker rm anyproxy-demo-client
```

## 🔗 Next Steps

- **Full Setup Guide**: See [main README](../README.md) for complete deployment
- **Examples**: Check [examples/](../examples/) for more configurations
- **Issues**: Report problems at [GitHub Issues](https://github.com/buhuipao/anyproxy/issues) 