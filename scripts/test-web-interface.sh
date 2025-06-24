#!/bin/bash

# Test Script for AnyProxy Web Management Interface
# This script validates the web interface functionality described in the documentation

set -e

echo "🚀 AnyProxy Web Interface Test Script"
echo "====================================="

# Configuration
GATEWAY_PORT=8090
CLIENT_PORT=8091
GATEWAY_USERNAME="admin"
GATEWAY_PASSWORD="admin123"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Helper functions
log_info() {
    echo -e "${BLUE}ℹ️  $1${NC}"
}

log_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

log_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

log_error() {
    echo -e "${RED}❌ $1${NC}"
}

# Test web service availability
test_service_availability() {
    local service=$1
    local port=$2
    local url="http://localhost:$port"
    
    log_info "Testing $service availability on port $port"
    
    if curl -s --connect-timeout 5 "$url" > /dev/null 2>&1; then
        log_success "$service is accessible at $url"
        return 0
    else
        log_error "$service is not accessible at $url"
        return 1
    fi
}

# Test Gateway API endpoints
test_gateway_api() {
    local base_url="http://localhost:$GATEWAY_PORT"
    
    log_info "Testing Gateway API endpoints"
    
    # Test authentication check (should return unauthenticated)
    if curl -s "$base_url/api/auth/check" | grep -q "authenticated"; then
        log_success "Gateway auth check endpoint is working"
    else
        log_warning "Gateway auth check endpoint may not be working"
    fi
    
    # Test login endpoint
    local login_response=$(curl -s -X POST "$base_url/api/auth/login" \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"$GATEWAY_USERNAME\",\"password\":\"$GATEWAY_PASSWORD\"}")
    
    if echo "$login_response" | grep -q "success"; then
        log_success "Gateway login endpoint is working"
        
        # Extract session cookie for authenticated requests
        local session_cookie=$(curl -s -c - -X POST "$base_url/api/auth/login" \
            -H "Content-Type: application/json" \
            -d "{\"username\":\"$GATEWAY_USERNAME\",\"password\":\"$GATEWAY_PASSWORD\"}" \
            | grep "gateway_session_id" | awk '{print $7}')
        
        if [ -n "$session_cookie" ]; then
            # Test protected endpoints with authentication
            log_info "Testing protected Gateway API endpoints"
            
            if curl -s -b "gateway_session_id=$session_cookie" "$base_url/api/metrics/global" | grep -q "active_connections"; then
                log_success "Gateway global metrics endpoint is working"
            else
                log_warning "Gateway global metrics endpoint may not be working"
            fi
            
            if curl -s -b "gateway_session_id=$session_cookie" "$base_url/api/metrics/clients" > /dev/null; then
                log_success "Gateway client metrics endpoint is working"
            else
                log_warning "Gateway client metrics endpoint may not be working"
            fi
            
            if curl -s -b "gateway_session_id=$session_cookie" "$base_url/api/metrics/connections" > /dev/null; then
                log_success "Gateway connection metrics endpoint is working"
            else
                log_warning "Gateway connection metrics endpoint may not be working"
            fi
        fi
    else
        log_warning "Gateway login endpoint authentication failed - check credentials"
    fi
}

# Test Client API endpoints
test_client_api() {
    local base_url="http://localhost:$CLIENT_PORT"
    
    log_info "Testing Client API endpoints"
    
    # Test status endpoint (usually no authentication required)
    if curl -s "$base_url/api/status" | grep -q "client_id\|status"; then
        log_success "Client status endpoint is working"
    else
        log_warning "Client status endpoint may not be working"
    fi
    
    # Test connection metrics endpoint
    if curl -s "$base_url/api/metrics/connections" > /dev/null; then
        log_success "Client connection metrics endpoint is working"
    else
        log_warning "Client connection metrics endpoint may not be working"
    fi
}

# Test static file access
test_static_files() {
    log_info "Testing static file access"
    
    # Test Gateway static files
    if curl -s "http://localhost:$GATEWAY_PORT/" | grep -q -i "html\|gateway\|anyproxy"; then
        log_success "Gateway static files are accessible"
    else
        log_warning "Gateway static files may not be accessible"
    fi
    
    # Test Client static files
    if curl -s "http://localhost:$CLIENT_PORT/" | grep -q -i "html\|client\|anyproxy"; then
        log_success "Client static files are accessible"
    else
        log_warning "Client static files may not be accessible"
    fi
    
    # Test i18n files
    if curl -s "http://localhost:$GATEWAY_PORT/js/i18n.js" | grep -q "I18n\|translation"; then
        log_success "Gateway i18n file is accessible"
    else
        log_warning "Gateway i18n file may not be accessible"
    fi
    
    if curl -s "http://localhost:$CLIENT_PORT/js/i18n.js" | grep -q "I18n\|translation"; then
        log_success "Client i18n file is accessible"
    else
        log_warning "Client i18n file may not be accessible"
    fi
}

# Display access information
display_access_info() {
    echo ""
    echo "🌐 Web Interface Access Information"
    echo "=================================="
    echo ""
    echo "Gateway Dashboard:"
    echo "  URL: http://localhost:$GATEWAY_PORT"
    echo "  Username: $GATEWAY_USERNAME"
    echo "  Password: $GATEWAY_PASSWORD"
    echo "  Features: Dashboard, authentication, metrics API"
    echo ""
    echo "Client Monitoring:"
    echo "  URL: http://localhost:$CLIENT_PORT"
    echo "  Authentication: Optional (check configuration)"
    echo "  Features: Status monitoring, connection tracking"
    echo ""
    echo "API Endpoints:"
    echo "  Gateway API: http://localhost:$GATEWAY_PORT/api/*"
    echo "  Client API: http://localhost:$CLIENT_PORT/api/*"
    echo ""
}

# Main test execution
main() {
    echo "Starting web interface validation..."
    echo ""
    
    # Test service availability
    gateway_available=false
    client_available=false
    
    if test_service_availability "Gateway Web Server" $GATEWAY_PORT; then
        gateway_available=true
    fi
    
    if test_service_availability "Client Web Server" $CLIENT_PORT; then
        client_available=true
    fi
    
    echo ""
    
    # Test APIs if services are available
    if [ "$gateway_available" = true ]; then
        test_gateway_api
        echo ""
    else
        log_warning "Skipping Gateway API tests - service not available"
        echo "To start Gateway web server, ensure gateway configuration includes:"
        echo "  web:"
        echo "    enabled: true"
        echo "    listen_addr: \":$GATEWAY_PORT\""
        echo "    auth_enabled: true"
        echo "    auth_username: \"$GATEWAY_USERNAME\""
        echo "    auth_password: \"$GATEWAY_PASSWORD\""
        echo ""
    fi
    
    if [ "$client_available" = true ]; then
        test_client_api
        echo ""
    else
        log_warning "Skipping Client API tests - service not available"
        echo "To start Client web server, ensure client configuration includes:"
        echo "  web:"
        echo "    enabled: true"
        echo "    listen_addr: \":$CLIENT_PORT\""
        echo ""
    fi
    
    # Test static files if any service is available
    if [ "$gateway_available" = true ] || [ "$client_available" = true ]; then
        test_static_files
        echo ""
    fi
    
    # Display access information
    display_access_info
    
    echo "🎯 Next Steps:"
    echo "============="
    echo "1. Open the Gateway dashboard in your browser"
    echo "2. Login with the provided credentials"
    echo "3. Test the language switching functionality"
    echo "4. Monitor real-time metrics and client connections"
    echo "5. Check the Client monitoring interface"
    echo ""
    echo "📚 For more information, see:"
    echo "   - web/README.md - Complete web interface documentation"
    echo "   - README.md - Main project documentation"
    echo ""
}

# Run main function
main 