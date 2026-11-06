#!/bin/bash
# AnyProxy 延迟与吞吐量测试脚本
# Usage: ./run_tests.sh [-a|-q|-s|-l] [proxy_addr]

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# 默认配置
LOCAL_PROXY="127.0.0.1:1080"
PROXY_ADDR="$LOCAL_PROXY"
LOCAL_MODE=true
GATEWAY_PID=""
CLIENT_PID=""

# 进入测试目录
cd "$(dirname "$0")"
SCRIPT_DIR="$(pwd)"
PROJECT_ROOT="$(cd ../.. && pwd)"

show_help() {
    echo "AnyProxy 延迟与吞吐量测试工具"
    echo ""
    echo "用法: $0 [选项]"
    echo ""
    echo "选项:"
    echo "  -a, --all     运行所有测试"
    echo "  -q, --quick   快速游戏串流测试"
    echo "  -s, --stress  压力测试"
    echo "  -h, --help    显示帮助"
    exit 0
}

# 停止所有本地服务
stop_local_services() {
    echo -e "${YELLOW}停止现有服务...${NC}"
    pkill -9 -f "$SCRIPT_DIR/gateway" 2>/dev/null || true
    pkill -9 -f "$SCRIPT_DIR/client" 2>/dev/null || true
    sleep 2
    GATEWAY_PID=""
    CLIENT_PID=""
    echo -e "${GREEN}服务已停止${NC}"
}

# 每次测试前强制删除并重新编译所有二进制
rebuild_binaries() {
    echo -e "${YELLOW}强制重新编译 Gateway / Client / latency...${NC}"

    # Build gateway/client in project root
    cd "$PROJECT_ROOT"
    rm -f $SCRIPT_DIR/gateway $SCRIPT_DIR/client
    go build -o $SCRIPT_DIR/gateway ./cmd/gateway
    go build -o $SCRIPT_DIR/client ./cmd/client

    # Build latency test tool in test dir
    cd "$SCRIPT_DIR"
    rm -f $SCRIPT_DIR/latency
    go build -o $SCRIPT_DIR/latency .

    echo -e "${GREEN}编译完成${NC}"
}

# 启动本地 Gateway
start_local_gateway() {
    echo -e "${YELLOW}启动 Gateway...${NC}"
    cd "$PROJECT_ROOT"

    rm -f /tmp/gateway.log
    nohup $SCRIPT_DIR/gateway -config "$SCRIPT_DIR/config.yaml" > /tmp/gateway.log 2>&1 &
    GATEWAY_PID=$!
    sleep 3
    
    if kill -0 $GATEWAY_PID 2>/dev/null; then
        echo -e "${GREEN}Gateway 已启动 (PID: ${GATEWAY_PID})${NC}"
    else
        echo -e "${RED}Gateway 启动失败！${NC}"
        cat /tmp/gateway.log
        exit 1
    fi
    cd "$SCRIPT_DIR"
}

# 启动本地 Client
start_local_client() {
    echo -e "${YELLOW}启动 Client...${NC}"
    cd "$PROJECT_ROOT"

    rm -f /tmp/client.log
    nohup $SCRIPT_DIR/client -config "$SCRIPT_DIR/config.yaml" > /tmp/client.log 2>&1 &
    CLIENT_PID=$!
    sleep 5
    
    if kill -0 $CLIENT_PID 2>/dev/null; then
        local conn_count=$(grep -c "connection established" /tmp/client.log 2>/dev/null || echo "0")
        echo -e "${GREEN}Client 已启动 (PID: ${CLIENT_PID}), 连接数: ${conn_count}${NC}"
    else
        echo -e "${RED}Client 启动失败！${NC}"
        cat /tmp/client.log
        exit 1
    fi
    cd "$SCRIPT_DIR"
}

# 重启所有服务
restart_services() {
    echo -e "\n${BLUE}======== 重启 Gateway 和 Client ========${NC}\n"
    stop_local_services
    rebuild_binaries
    start_local_gateway
    start_local_client
    echo -e "\n${GREEN}✓ 所有服务已就绪${NC}\n"
}

cleanup() {
    echo -e "\n${YELLOW}清理测试环境...${NC}"
    stop_local_services
    echo -e "${GREEN}清理完成${NC}"
}
trap cleanup EXIT

# 解析参数
TEST_MODE=""
for arg in "$@"; do
    case $arg in
        -h|--help) show_help ;;
        -a|--all) TEST_MODE="all" ;;
        -q|--quick) TEST_MODE="quick" ;;
        -s|--stress) TEST_MODE="stress" ;;
    esac
done

# 打印标题
echo -e "${BLUE}============================================${NC}"
echo -e "${BLUE}       AnyProxy 延迟与吞吐量测试${NC}"
echo -e "${BLUE}============================================${NC}"
echo -e "SOCKS5 代理: ${GREEN}${PROXY_ADDR}${NC}"
echo ""

run_all_tests() {
    restart_services
    echo -e "${BLUE}=== 运行所有测试 ===${NC}"
    $SCRIPT_DIR/latency -test all -proxy "$PROXY_ADDR" -user demo-group -skip-direct
}

run_quick_test() {
    restart_services
    echo -e "${BLUE}=== 快速测试 ===${NC}"
    $SCRIPT_DIR/latency -test gamestream -proxy "$PROXY_ADDR" -user demo-group -skip-direct
}

run_stress_test() {
    restart_services
    echo -e "${BLUE}=== 压力测试 ===${NC}"
    $SCRIPT_DIR/latency -test stress -proxy "$PROXY_ADDR" -user demo-group -skip-direct
}

case $TEST_MODE in
    "all") run_all_tests ;;
    "quick") run_quick_test ;;
    "stress") run_stress_test ;;
    *)
        while true; do
            echo -e "${BLUE}============================================${NC}"
            echo "  1) 运行所有测试"
            echo "  2) 快速测试"
            echo "  3) 压力测试"
            echo "  4) 退出"
            echo -n "请选择 [1-4]: "
            read -r choice
            case $choice in
                1) run_all_tests ;;
                2) run_quick_test ;;
                3) run_stress_test ;;
                4) echo -e "${GREEN}再见！${NC}"; exit 0 ;;
                *) echo -e "${RED}无效选择${NC}" ;;
            esac
        done
        ;;
esac

echo -e "${GREEN}✅ 测试完成${NC}"
