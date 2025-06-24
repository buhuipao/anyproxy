# AnyProxy Web Management Interfaces

AnyProxy provides comprehensive web management interfaces with real-time monitoring, session-based authentication, and automatic metrics collection with intelligent cleanup.

## 🌟 Features

### Gateway Web Interface
- **Real-time Monitoring**: Active connections, data transfer statistics, success rate monitoring
- **Client Management**: View all connected client status and traffic statistics with online/offline detection
- **Automatic Metrics Collection**: Memory-based metrics with 2-minute offline detection and 3-minute cleanup
- **Session-based Authentication**: 24-hour sessions with automatic renewal and secure cookie management
- **Internationalization Support**: Complete bilingual interface (English/Chinese) with persistent preferences
- **Responsive Design**: Mobile-friendly interface with modern UI components

### Client Web Interface
- **Local Monitoring**: Client runtime status and connection information
- **Connection Tracking**: Real-time view of active connections and traffic statistics
- **Multi-client Support**: Track multiple client instances from single interface
- **Optional Authentication**: Configurable authentication with session management
- **Auto-refresh**: Configurable real-time data updates with manual control

## 🚀 Quick Start

### 1. Start Services

Use the provided test script to start quickly:

```bash
# Start Gateway and Client services (including Web interfaces)
./scripts/test-web-interface.sh
```

### 2. Access Web Interfaces

**Gateway Management Interface**
- URL: http://localhost:8090
- Username: `admin`
- Password: `admin123`
- Features: Dashboard, login page, real-time metrics

**Client Monitoring Interface**
- URL: http://localhost:8091
- No authentication required (by default)
- Features: Status monitoring, connection tracking

### 3. Validate Setup

Run the validation script to test all documented features:

```bash
# Test web interface functionality and API endpoints
./scripts/test-web-interface.sh
```

This script will:
- ✅ Check web service availability on both ports
- ✅ Test all documented API endpoints
- ✅ Validate authentication functionality
- ✅ Verify static file access and i18n support
- ✅ Provide troubleshooting guidance if issues are found

## 📱 Interface Components

### Gateway Dashboard (`dashboard.html`)
- **Statistics Cards**: Real-time display of active connections, total connections, data transfer, success rate
- **Client Status Table**: Live monitoring of all client connection status and traffic with online/offline indicators
- **Auto Refresh**: Configurable 10-second auto refresh with manual toggle
- **Language Switch**: One-click switching between English and Chinese with persistent storage

### Client Monitoring (`index.html`)
- **Runtime Status**: Display client uptime and basic connection statistics
- **Connection List**: Detailed view of all active connections with traffic breakdown
- **Multi-client Tracking**: Support for monitoring multiple client instances
- **System Information**: Client ID, runtime, and connection metrics

### Authentication (`login.html`)
- **Secure Login**: Session-based authentication with 24-hour timeout
- **Internationalized**: Bilingual login interface with error handling
- **Security Features**: HttpOnly cookies, CSRF protection, session management

## ⚙️ Configuration Options

### Gateway Web Configuration

```yaml
gateway:
  web:
    enabled: true                    # Enable Web interface
    listen_addr: ":8090"            # Listen address
    static_dir: "web/gateway/static" # Static files directory
    auth_enabled: true              # Enable authentication
    auth_username: "admin"          # Username
    auth_password: "admin123"       # Password
```

### Client Web Configuration

```yaml
client:
  web:
    enabled: true                   # Enable Web interface
    listen_addr: ":8091"           # Listen address
    static_dir: "web/client/static" # Static files directory
    auth_enabled: false             # Optional authentication
    auth_username: "client"         # Username (if auth enabled)
    auth_password: "password"       # Password (if auth enabled)
```

## 🔧 API Interfaces

### Gateway API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/auth/login` | POST | User login (creates 24-hour session) |
| `/api/auth/logout` | POST | User logout (destroys session) |
| `/api/auth/check` | GET | Check authentication status |
| `/api/metrics/global` | GET | Global statistics (active connections, data transfer, success rate) |
| `/api/metrics/clients` | GET | All client statistics with online/offline status |
| `/api/metrics/connections` | GET | Active connection details and metrics |

### Client API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/auth/login` | POST | User login (if authentication enabled) |
| `/api/auth/logout` | POST | User logout (if authentication enabled) |
| `/api/auth/check` | GET | Check authentication status |
| `/api/status` | GET | Client status with runtime metrics and connection summary |
| `/api/metrics/connections` | GET | Connection metrics for all tracked client instances |

## 🌍 Internationalization Support

Complete bilingual support implemented via `i18n.js`:

- **Languages**: English (default) and Chinese
- **Persistent Selection**: Language preference saved in browser localStorage
- **Complete Coverage**: All UI elements, error messages, and formatting
- **Switch Method**: Click the language toggle button in the interface header
- **Localized Formatting**: Numbers, dates, file sizes, and durations
- **Real-time Updates**: Dynamic language switching without page reload

### I18n Features
- Automatic browser language detection
- Fallback to English for missing translations
- Parameter substitution support
- Consistent terminology across interfaces
- Cultural adaptation for date/time formatting

## 📊 Monitoring System

### Memory-based Metrics
- **Real-time Collection**: All metrics stored in memory with atomic operations
- **Connection Tracking**: Individual connection lifecycle management
- **Client Status**: Online/offline detection with automatic cleanup
- **Data Transfer**: Byte-level tracking for sent/received data
- **Error Tracking**: Connection failure and error rate monitoring

### Automatic Cleanup Process
- **Offline Detection**: Clients marked offline after 2 minutes of inactivity
- **Cleanup Interval**: Runs every 10 seconds for responsive updates
- **Stale Connection Removal**: Automatic cleanup of connections from offline clients
- **Metrics Validation**: Periodic validation and auto-correction of connection counts
- **Memory Management**: Efficient cleanup to prevent memory leaks

### Performance Optimizations
- **Atomic Operations**: Thread-safe counters for concurrent access
- **Connection Pooling**: Efficient connection state management
- **Batch Updates**: Optimized metric updates for high-throughput scenarios
- **Consistency Checks**: Automatic detection and correction of inconsistent states

## 🔒 Security Features

### Authentication System
- **Session Management**: 24-hour sessions with automatic renewal
- **Secure Cookies**: HttpOnly, Secure, SameSite protection
- **Session Cleanup**: Automatic removal of expired sessions every 5 minutes
- **Failed Login Tracking**: Audit logging of failed authentication attempts

### Authorization
- **Protected Routes**: API endpoints protected by authentication middleware
- **Public Assets**: Static files (CSS, JS, images) accessible for login page
- **CORS Support**: Configurable cross-origin resource sharing
- **Request Validation**: Input sanitization and validation

### Data Protection
- **No Group ID Exposure**: Sensitive client grouping information excluded from API responses
- **Minimal Data Exposure**: Only necessary metrics exposed via API
- **Secure Error Handling**: Prevent information leakage through error messages

## 📱 Responsive Design

### Mobile Compatibility
- **Adaptive Layout**: Automatic adjustment for different screen sizes
- **Touch-friendly**: Optimized for touch interactions
- **Readable Typography**: Responsive font sizes and spacing
- **Efficient Navigation**: Mobile-optimized menu and button layouts

### Modern UI Components
- **Clean Design**: Modern card-based layout with consistent spacing
- **Status Indicators**: Visual connection status with color coding
- **Data Visualization**: Clear presentation of metrics and statistics
- **Interactive Elements**: Hover effects and smooth animations

## 🚨 Troubleshooting

### Common Issues

1. **Cannot access Web interface**
   - Check if ports are accessible: `curl http://localhost:8090` (Gateway) or `curl http://localhost:8091` (Client)
   - Verify service startup: Check logs for "Starting Gateway/Client Web server" message
   - Confirm configuration: Ensure `web.enabled: true` in config file

2. **Authentication issues**
   - Gateway login fails: Verify username/password in config match login credentials
   - Session expires: Check if session timeout (24 hours) has been exceeded
   - Cookie issues: Clear browser cookies and try again

3. **Missing or incorrect data**
   - Empty metrics: Ensure Gateway and Client are connected and processing traffic
   - Client shows offline: Check if client hasn't been active for >2 minutes
   - Inconsistent connection counts: Metrics system automatically detects and corrects these

4. **Language switching problems**
   - Language not persisting: Check if browser localStorage is enabled
   - Incomplete translation: Verify `i18n.js` is loaded correctly
   - Browser compatibility: Ensure modern browser with JavaScript enabled

### Debug Information

**Log Locations**
- Gateway logs: `logs/anyproxy.log`
- Client logs: `logs/anyproxy.log`
- Web server logs: Console output during startup

**Browser Console**
- Check for JavaScript errors that might affect functionality
- Monitor API requests for authentication or network issues
- Verify WebSocket connections for real-time updates

## 🔄 Development and Customization

### Static File Structure
```
web/
├── gateway/static/
│   ├── dashboard.html       # Main dashboard interface
│   ├── login.html          # Authentication page
│   ├── index.html          # Landing page
│   └── js/i18n.js          # Internationalization
└── client/static/
    ├── index.html          # Client monitoring interface
    ├── login.html          # Authentication page (if enabled)
    └── js/i18n.js          # Internationalization
```

### Adding Custom Translations
1. Edit `web/gateway/static/js/i18n.js` or `web/client/static/js/i18n.js`
2. Add new translation keys to both `en` and `zh` objects
3. Use `data-i18n="your.key"` in HTML elements
4. Restart the web server to load changes

### API Integration
- RESTful JSON APIs with CORS support
- Consistent error handling with HTTP status codes
- Real-time data updates with configurable refresh intervals
- Secure authentication tokens for API access

## 🎯 Best Practices

### Security
- Always enable authentication in production environments
- Use HTTPS with valid certificates for production deployments
- Regularly rotate authentication credentials
- Monitor logs for suspicious login attempts

### Performance
- Configure appropriate cleanup intervals based on client count
- Monitor memory usage with many connected clients
- Use auto-refresh judiciously to avoid overwhelming the server
- Implement rate limiting for API endpoints in high-traffic scenarios

### Maintenance
- Regularly check logs for errors or warnings
- Monitor client connection patterns and cleanup efficiency
- Update translation files when adding new features
- Test authentication and session management periodically 