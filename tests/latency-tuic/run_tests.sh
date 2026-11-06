#!/bin/bash
# TUIC v5 Latency Test Runner
# This script provides convenient test execution for common scenarios
#
# Features:
# - Auto-kills previous anyproxy processes
# - Rebuilds binaries before testing
# - Starts anyproxy-client for E2E testing

set -e

# Default configuration
SERVER="${TUIC_SERVER:-127.0.0.1:9443}"
UUID="${TUIC_UUID:-demo-group}"
PASSWORD="${TUIC_PASSWORD:-demo-123}"
MODE="${TUIC_MODE:-native}"
CONGESTION="${TUIC_CONGESTION:-bbr}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Get script directory and project root
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$SCRIPT_DIR/../.."
CLIENT_PID=""
CLIENT_LOG=""

print_header() {
    echo -e "${BLUE}╔══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║          TUIC v5 Latency Test Runner                         ║${NC}"
    echo -e "${BLUE}╚══════════════════════════════════════════════════════════════╝${NC}"
    echo ""
}

# Cleanup function - called on exit
cleanup() {
    echo ""
    echo -e "${YELLOW}Cleaning up...${NC}"
    
    # Kill client if we started it
    if [[ -n "$CLIENT_PID" ]] && kill -0 "$CLIENT_PID" 2>/dev/null; then
        echo -e "${YELLOW}Stopping AnyProxy client (PID: $CLIENT_PID)...${NC}"
        kill "$CLIENT_PID" 2>/dev/null || true
        wait "$CLIENT_PID" 2>/dev/null || true
    fi
    
    # Show client log location if exists
    if [[ -n "$CLIENT_LOG" && -f "$CLIENT_LOG" ]]; then
        echo -e "${BLUE}Client logs saved to: $CLIENT_LOG${NC}"
    fi
}

# Set trap for cleanup
trap cleanup EXIT INT TERM

# Kill existing anyproxy processes
kill_existing_processes() {
    echo -e "${YELLOW}Killing existing anyproxy processes...${NC}"
    
    # Kill anyproxy-client
    pkill -f "anyproxy-client" 2>/dev/null || true
    
    # Kill anyproxy-gateway (if running locally for testing)
    # pkill -f "anyproxy-gateway" 2>/dev/null || true
    
    # Kill any go run instances
    pkill -f "go run.*anyproxy" 2>/dev/null || true
    
    # Wait a moment for processes to die
    sleep 1
    
    echo -e "${GREEN}Done killing existing processes${NC}"
}

# Build binaries
build_binaries() {
    echo -e "${YELLOW}Building anyproxy binaries...${NC}"
    cd "$PROJECT_ROOT"
    
    make build
    
    if [[ ! -f "./bin/anyproxy-client" ]]; then
        echo -e "${RED}Error: anyproxy-client binary not found after build${NC}"
        exit 1
    fi
    
    echo -e "${GREEN}Build completed successfully${NC}"
    cd "$SCRIPT_DIR"
}

# Start the anyproxy client
start_client() {
    echo -e "${YELLOW}Starting AnyProxy client...${NC}"
    
    # Create temp log file for client output
    CLIENT_LOG="/tmp/anyproxy-client-$(date +%Y%m%d%H%M%S).log"
    touch "$CLIENT_LOG"
    
    cd "$PROJECT_ROOT"
    ./bin/anyproxy-client -config tests/latency-tuic/client-config.yaml > "$CLIENT_LOG" 2>&1 &
    CLIENT_PID=$!
    
    echo -e "${GREEN}Client started with PID: $CLIENT_PID${NC}"
    echo -e "${BLUE}Client logs: $CLIENT_LOG${NC}"
    
    # Wait for client to connect
    echo -e "${YELLOW}Waiting for client to connect to gateway...${NC}"
    sleep 3
    
    # Verify client is still running
    if ! kill -0 "$CLIENT_PID" 2>/dev/null; then
        echo -e "${RED}Error: Client process died unexpectedly${NC}"
        echo -e "${RED}Check logs: $CLIENT_LOG${NC}"
        # Show last 20 lines of log for debugging
        echo -e "${YELLOW}Last 20 lines of client log:${NC}"
        tail -20 "$CLIENT_LOG"
        exit 1
    fi
    
    cd "$SCRIPT_DIR"
}

print_usage() {
    echo "Usage: $0 [COMMAND] [OPTIONS]"
    echo ""
    echo "Commands:"
    echo "  all          Run ALL tests including stress (comprehensive, takes longer)"
    echo "  basic        Run latency, throughput, frameloss only (faster)"
    echo "  latency      Run latency tests only"
    echo "  throughput   Run throughput tests only"
    echo "  frameloss    Run frame loss tests only"
    echo "  gamestream   Run game streaming simulation"
    echo "  quick        Quick connectivity test"
    echo "  stress       High-throughput stress test (200/500/1000 Mbps + 60/120/240/360fps)"
    echo "  client       Start AnyProxy client only (for manual testing)"
    echo ""
    echo "Preset Scenarios:"
    echo "  1080p60      Test 1080p 60fps gaming (~20 Mbps)"
    echo "  1080p120     Test 1080p 120fps gaming (~40 Mbps)"
    echo "  2k60         Test 2K 60fps gaming (~40 Mbps)"
    echo "  2k120        Test 2K 120fps gaming (~80 Mbps) [Recommended for competitive]"
    echo "  4k60         Test 4K 60fps gaming (~50 Mbps)"
    echo "  4k120        Test 4K 120fps gaming (~150 Mbps) [Ultra high-end]"
    echo "  progressive  Progressive stress test: 1080p60 → 2K120 → 4K120"
    echo ""
    echo "Options:"
    echo "  -s, --server ADDRESS    TUIC server address (default: $SERVER)"
    echo "  -u, --uuid UUID         User UUID (default: $UUID)"
    echo "  -p, --password PASS     User password"
    echo "  -m, --mode MODE         UDP relay mode: native|quic (default: $MODE)"
    echo "  -d, --duration DURATION Test duration (default: 10s)"
    echo "  -f, --fps FPS           Target FPS (default: 60)"
    echo "  -b, --bitrate MBPS      Target bitrate in Mbps (default: 50)"
    echo "  --skip-direct           Skip direct baseline tests"
    echo "  --tls-cert FILE         Path to server's TLS certificate (default: certs/server.crt)"
    echo "  --no-build              Skip rebuilding binaries"
    echo "  --no-client             Don't start anyproxy-client (direct TUIC test only)"
    echo "  -h, --help              Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  TUIC_SERVER     Server address"
    echo "  TUIC_UUID       User UUID"
    echo "  TUIC_PASSWORD   User password"
    echo "  TUIC_MODE       UDP relay mode"
    echo "  TUIC_CONGESTION Congestion control algorithm"
    echo ""
    echo "Examples:"
    echo "  $0 all                                    # Run all tests with defaults"
    echo "  $0 latency -s myserver:9443 -u myuser    # Run latency tests"
    echo "  $0 stress -b 200 -d 30s                  # Stress test at 200Mbps for 30s"
    echo "  $0 2k120                                  # 2K 120Hz test"
    echo "  $0 client                                # Start client only"
}

# Parse command line arguments
COMMAND="all"
DURATION="10s"
FPS="60"
BITRATE="50"
SKIP_DIRECT=""
TLS_CERT=""
COUNT="20"
NO_BUILD=""
NO_CLIENT=""

# First argument is the command
if [[ $# -gt 0 && ! "$1" =~ ^- ]]; then
    COMMAND="$1"
    shift
fi

# Parse remaining arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -s|--server)
            SERVER="$2"
            shift 2
            ;;
        -u|--uuid)
            UUID="$2"
            shift 2
            ;;
        -p|--password)
            PASSWORD="$2"
            shift 2
            ;;
        -m|--mode)
            MODE="$2"
            shift 2
            ;;
        -d|--duration)
            DURATION="$2"
            shift 2
            ;;
        -f|--fps)
            FPS="$2"
            shift 2
            ;;
        -b|--bitrate)
            BITRATE="$2"
            shift 2
            ;;
        -c|--count)
            COUNT="$2"
            shift 2
            ;;
        --skip-direct)
            SKIP_DIRECT="-skip-direct"
            shift
            ;;
        --tls-cert)
            TLS_CERT="$2"
            shift 2
            ;;
        --no-build)
            NO_BUILD="1"
            shift
            ;;
        --no-client)
            NO_CLIENT="1"
            shift
            ;;
        -h|--help)
            print_header
            print_usage
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            print_usage
            exit 1
            ;;
    esac
done

# Validate required parameters
if [[ -z "$SERVER" ]]; then
    echo -e "${RED}Error: Server address is required${NC}"
    print_usage
    exit 1
fi

if [[ -z "$UUID" ]]; then
    echo -e "${RED}Error: UUID is required${NC}"
    print_usage
    exit 1
fi

if [[ -z "$PASSWORD" ]]; then
    echo -e "${RED}Error: Password is required${NC}"
    print_usage
    exit 1
fi

print_header

echo -e "${GREEN}Configuration:${NC}"
echo "  Server:     $SERVER"
echo "  UUID:       $UUID"
echo "  Mode:       $MODE"
echo "  Congestion: $CONGESTION"
echo ""

# Step 1: Kill existing processes
kill_existing_processes

# Step 2: Build binaries (unless --no-build)
if [[ -z "$NO_BUILD" ]]; then
    # build_binaries
    sleep 1
fi

# Build common arguments for test tool
COMMON_ARGS="-server $SERVER -uuid $UUID -password $PASSWORD -mode $MODE -congestion $CONGESTION"

# TLS configuration: use cert file
if [[ -n "$TLS_CERT" ]]; then
    COMMON_ARGS="$COMMON_ARGS -tls-cert $TLS_CERT"
else
    # Default: use the gateway's certificate from project root
    DEFAULT_CERT="$PROJECT_ROOT/certs/server.crt"
    if [[ -f "$DEFAULT_CERT" ]]; then
        echo -e "${GREEN}Using TLS certificate: $DEFAULT_CERT${NC}"
        COMMON_ARGS="$COMMON_ARGS -tls-cert $DEFAULT_CERT"
    else
        echo -e "${RED}Error: No TLS certificate found at $DEFAULT_CERT${NC}"
        echo -e "${YELLOW}Please specify --tls-cert or copy the server certificate to certs/server.crt${NC}"
        exit 1
    fi
fi

if [[ -n "$SKIP_DIRECT" ]]; then
    COMMON_ARGS="$COMMON_ARGS $SKIP_DIRECT"
fi

cd "$SCRIPT_DIR"

# Handle client-only command
if [[ "$COMMAND" == "client" ]]; then
    echo -e "${YELLOW}Starting AnyProxy client only (for manual testing)...${NC}"
    cd "$PROJECT_ROOT"
    exec ./bin/anyproxy-client -config tests/latency-tuic/client-config.yaml
fi

# Step 3: Start client (unless --no-client)
if [[ -z "$NO_CLIENT" ]]; then
    start_client
fi

# Step 4: Run the appropriate test
echo ""
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}  Starting Tests${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

case $COMMAND in
    all)
        echo -e "${YELLOW}Running ALL tests (comprehensive, this may take a while)...${NC}"
        go run . $COMMON_ARGS -test all -duration $DURATION -fps $FPS -bitrate $BITRATE -count $COUNT -stress-bitrates "200,500,1000"
        ;;
    basic)
        echo -e "${YELLOW}Running basic tests (latency, throughput, frameloss)...${NC}"
        go run . $COMMON_ARGS -test basic -duration $DURATION -fps $FPS -bitrate $BITRATE -count $COUNT
        ;;
    latency)
        echo -e "${YELLOW}Running latency tests...${NC}"
        go run . $COMMON_ARGS -test latency -count $COUNT
        ;;
    throughput)
        echo -e "${YELLOW}Running throughput tests...${NC}"
        go run . $COMMON_ARGS -test throughput -duration $DURATION -bitrate $BITRATE
        ;;
    frameloss)
        echo -e "${YELLOW}Running frame loss tests...${NC}"
        go run . $COMMON_ARGS -test frameloss -duration $DURATION -fps $FPS
        ;;
    gamestream)
        echo -e "${YELLOW}Running game streaming simulation...${NC}"
        go run . $COMMON_ARGS -test gamestream -duration $DURATION
        ;;
    quick)
        echo -e "${YELLOW}Running quick connectivity test...${NC}"
        go run . $COMMON_ARGS -test latency -count 5 -skip-direct
        ;;
    stress)
        echo -e "${YELLOW}Running HIGH THROUGHPUT stress test (200, 500, 1000 Mbps)...${NC}"
        echo -e "${BLUE}Testing latency, throughput, and frame loss at extreme bitrates${NC}"
        go run . $COMMON_ARGS -test stress -duration $DURATION -fps $FPS -stress-bitrates "200,500,1000"
        ;;
    1080p60)
        echo -e "${YELLOW}Running 1080p 60fps gaming test (20 Mbps)...${NC}"
        go run . $COMMON_ARGS -test all -fps 60 -bitrate 20 -duration $DURATION
        ;;
    1080p120)
        echo -e "${YELLOW}Running 1080p 120fps gaming test (40 Mbps)...${NC}"
        go run . $COMMON_ARGS -test all -fps 120 -bitrate 40 -duration $DURATION
        ;;
    2k60)
        echo -e "${YELLOW}Running 2K 60fps gaming test (40 Mbps)...${NC}"
        go run . $COMMON_ARGS -test all -fps 60 -bitrate 40 -duration $DURATION
        ;;
    2k120)
        echo -e "${YELLOW}Running 2K 120fps competitive gaming test (80 Mbps)...${NC}"
        echo -e "${BLUE}This is the recommended test for 2K 120Hz gaming experience${NC}"
        go run . $COMMON_ARGS -test all -fps 120 -bitrate 80 -duration 30s
        ;;
    4k60)
        echo -e "${YELLOW}Running 4K 60fps gaming test (50 Mbps)...${NC}"
        go run . $COMMON_ARGS -test all -fps 60 -bitrate 50 -duration $DURATION
        ;;
    4k120)
        echo -e "${YELLOW}Running 4K 120fps ultra high-end gaming test (150 Mbps)...${NC}"
        echo -e "${RED}Warning: Requires excellent network connection!${NC}"
        go run . $COMMON_ARGS -test all -fps 120 -bitrate 150 -duration 30s
        ;;
    progressive)
        echo -e "${YELLOW}========================================${NC}"
        echo -e "${YELLOW}Progressive Stress Test${NC}"
        echo -e "${YELLOW}Testing from 1080p60 to 4K120 step by step${NC}"
        echo -e "${YELLOW}========================================${NC}"
        
        echo ""
        echo -e "${GREEN}[Step 1/5] 1080p 60Hz - Basic gaming (25 Mbps)${NC}"
        go run . $COMMON_ARGS -test all -fps 60 -bitrate 25 -duration 15s
        
        echo ""
        echo -e "${GREEN}[Step 2/5] 1080p 120Hz - Competitive gaming (40 Mbps)${NC}"
        go run . $COMMON_ARGS -test all -fps 120 -bitrate 40 -duration 15s
        
        echo ""
        echo -e "${GREEN}[Step 3/5] 2K 60Hz - High quality gaming (40 Mbps)${NC}"
        go run . $COMMON_ARGS -test all -fps 60 -bitrate 40 -duration 15s
        
        echo ""
        echo -e "${GREEN}[Step 4/5] 2K 120Hz - Competitive high quality (80 Mbps)${NC}"
        go run . $COMMON_ARGS -test all -fps 120 -bitrate 80 -duration 20s
        
        echo ""
        echo -e "${GREEN}[Step 5/5] 4K 120Hz - Ultra high-end (150 Mbps)${NC}"
        go run . $COMMON_ARGS -test all -fps 120 -bitrate 150 -duration 20s
        
        echo ""
        echo -e "${YELLOW}========================================${NC}"
        echo -e "${YELLOW}Progressive test completed!${NC}"
        echo -e "${YELLOW}========================================${NC}"
        ;;
    *)
        echo -e "${RED}Unknown command: $COMMAND${NC}"
        print_usage
        exit 1
        ;;
esac

echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  Test completed!${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
