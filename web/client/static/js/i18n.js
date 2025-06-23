// Internationalization (i18n) Support for AnyProxy Client
class I18n {
    constructor() {
        this.currentLanguage = this.detectLanguage();
        this.translations = {
            'en': {
                // Common
                'common.language_switch': '中文',
                'common.refresh': 'Refresh Data',
                'common.auto_refresh': 'Auto Refresh (10s)',
                'common.loading': 'Loading...',
                'common.online': 'Online',
                'common.offline': 'Offline',
                'common.active': 'Active',
                'common.inactive': 'Inactive',
                'common.healthy': 'Healthy',
                'common.warning': 'Warning',
                'common.error': 'Error',
                'common.unknown': 'Unknown',

                // Client
                'client.title': 'AnyProxy Client Dashboard',
                'client.subtitle': 'Client Management Interface - Local Monitoring',
                'client.welcome': 'Welcome, ',
                'client.logout': 'Logout',
                'client.status.running_status': 'Running Status',
                'client.status.running': 'Running',
                'client.status.stopped': 'Stopped',
                'client.status.uptime': 'Uptime',
                'client.status.gateway_connecting': 'Gateway Connecting...',
                'client.status.gateway_healthy': 'Gateway Connected',
                'client.status.gateway_warning': 'Gateway Warning',
                'client.status.gateway_error': 'Gateway Error',
                'client.status.gateway_disconnected': 'Gateway Disconnected',

                // Metrics
                'metrics.active_connections': 'Active Connections',
                'metrics.total_connections': 'Total Connections',
                'metrics.bytes_sent': 'Data Sent',
                'metrics.bytes_received': 'Data Received',
                'metrics.success_rate': 'Success Rate',

                // Connections
                'client.connections.title': 'Active Connections',
                'client.connections.connection_id': 'Connection ID',
                'client.connections.target_host': 'Target Host',
                'client.connections.protocol': 'Protocol',
                            'client.connections.bytes_sent': 'Sent',
            'client.connections.bytes_received': 'Received',
            'client.connections.duration': 'Duration',
            'client.connections.status': 'Status',
            'client.connections.no_connections': 'No active connections',
            'client.status.gateway_status': 'Gateway Status',

                // Health Check
                'client.health.title': 'Health Check',
                'client.health.check_item': 'Check Item',
                'client.health.status': 'Status',
                'client.health.details': 'Details',
                'client.health.last_check': 'Last Check',
                'client.health.gateway_connection': 'Gateway Connection',
                'client.health.local_service': 'Local Service',
                'client.health.connection_stable': 'Connection stable',
                'client.health.services_reachable': 'All configured services reachable',

                // Diagnostics
                'client.diagnostics.title': 'System Information',
                'client.diagnostics.client_id': 'Client ID',
                'client.diagnostics.version': 'Version',
                'client.diagnostics.uptime': 'Uptime',
                'client.diagnostics.connections': 'Active Connections',
                'client.diagnostics.sent': 'Network Sent',
                'client.diagnostics.received': 'Network Received',
                'client.diagnostics.errors': 'Error Count',

                // Login
                'login.title': 'AnyProxy Client - Login',
                'login.welcome': 'AnyProxy',
                'login.subtitle': 'Client Management System',
                'login.username': 'Username',
                'login.password': 'Password',
                'login.button': 'Login',
                'login.logging_in': 'Logging in...',
                'login.error.invalid': 'Invalid username or password',
                'login.error.network': 'Network error, please try again later',
                'login.footer': '© 2025 AnyProxy. Authentication required to access client interface.',

                // API Errors
                'api.error.unauthorized': 'Authentication required',
                'api.error.forbidden': 'Access denied',
                'api.error.not_found': 'Resource not found',
                'api.error.server': 'Server error',
                'api.error.network': 'Network error'
            },
            'zh': {
                // Common
                'common.language_switch': 'English',
                'common.refresh': '刷新数据',
                'common.auto_refresh': '自动刷新 (10秒)',
                'common.loading': '加载中...',
                'common.online': '在线',
                'common.offline': '离线',
                'common.active': '活跃',
                'common.inactive': '非活跃',
                'common.healthy': '正常',
                'common.warning': '警告',
                'common.error': '错误',
                'common.unknown': '未知',

                // Client
                'client.title': 'AnyProxy 客户端控制台',
                'client.subtitle': '客户端管理界面 - 本地监控',
                'client.welcome': '欢迎，',
                'client.logout': '登出',
                'client.status.running_status': '运行状态',
                'client.status.running': '运行中',
                'client.status.stopped': '已停止',
                'client.status.uptime': '运行时间',
                'client.status.gateway_connecting': '网关连接中...',
                'client.status.gateway_healthy': '网关已连接',
                'client.status.gateway_warning': '网关警告',
                'client.status.gateway_error': '网关错误',
                'client.status.gateway_disconnected': '网关已断开',

                // Metrics
                'metrics.active_connections': '活跃连接',
                'metrics.total_connections': '总连接数',
                'metrics.bytes_sent': '发送数据',
                'metrics.bytes_received': '接收数据',
                'metrics.success_rate': '成功率',

                // Connections
                'client.connections.title': '活跃连接',
                'client.connections.connection_id': '连接ID',
                'client.connections.target_host': '目标主机',
                'client.connections.protocol': '协议',
                'client.connections.bytes_sent': '发送',
                'client.connections.bytes_received': '接收',
                'client.connections.duration': '持续时间',
                'client.connections.status': '状态',
                'client.connections.no_connections': '暂无活跃连接',
                'client.status.gateway_status': '网关状态',

                // Health Check
                'client.health.title': '健康检查',
                'client.health.check_item': '检查项',
                'client.health.status': '状态',
                'client.health.details': '详情',
                'client.health.last_check': '最后检查',
                'client.health.gateway_connection': '网关连接',
                'client.health.local_service': '本地服务',
                'client.health.connection_stable': '连接稳定',
                'client.health.services_reachable': '所有配置的服务可达',

                // Diagnostics
                'client.diagnostics.title': '系统信息',
                'client.diagnostics.client_id': '客户端ID',
                'client.diagnostics.version': '版本',
                'client.diagnostics.uptime': '运行时间',
                'client.diagnostics.connections': '活跃连接',
                'client.diagnostics.sent': '网络发送',
                'client.diagnostics.received': '网络接收',
                'client.diagnostics.errors': '错误次数',

                // Login
                'login.title': 'AnyProxy 客户端 - 登录',
                'login.welcome': 'AnyProxy',
                'login.subtitle': '客户端管理系统',
                'login.username': '用户名',
                'login.password': '密码',
                'login.button': '登录',
                'login.logging_in': '登录中...',
                'login.error.invalid': '用户名或密码错误',
                'login.error.network': '网络错误，请稍后重试',
                'login.footer': '© 2025 AnyProxy. 访问客户端界面需要认证。',

                // API Errors
                'api.error.unauthorized': '需要认证',
                'api.error.forbidden': '访问被拒绝',
                'api.error.not_found': '资源未找到',
                'api.error.server': '服务器错误',
                'api.error.network': '网络错误'
            }
        };

        // Apply translations when page loads
        this.applyTranslations();
    }

    detectLanguage() {
        // Check localStorage first
        const savedLang = localStorage.getItem('preferred-language');
        if (savedLang) {
            return savedLang;
        }

        // Check browser language
        const browserLang = navigator.language || navigator.userLanguage;
        if (browserLang.startsWith('zh')) {
            return 'zh';
        }
        return 'en';
    }

    t(key, params = {}) {
        const translation = this.translations[this.currentLanguage][key] || key;
        
        // Simple parameter substitution
        let result = translation;
        Object.keys(params).forEach(param => {
            result = result.replace(`{{${param}}}`, params[param]);
        });
        
        return result;
    }

    applyTranslations() {
        // Update document title
        const titleElement = document.querySelector('[data-i18n-document-title]');
        if (titleElement) {
            document.title = this.t(titleElement.getAttribute('data-i18n-document-title'));
        }

        // Update all elements with data-i18n attribute
        document.querySelectorAll('[data-i18n]').forEach(element => {
            const key = element.getAttribute('data-i18n');
            const paramsAttr = element.getAttribute('data-i18n-params');
            let params = {};
            
            if (paramsAttr) {
                try {
                    params = JSON.parse(paramsAttr);
                } catch (e) {
                    console.warn('Invalid i18n params:', paramsAttr);
                }
            }
            
            if (element.tagName === 'INPUT' && element.hasAttribute('placeholder')) {
                element.placeholder = this.t(key, params);
            } else {
                element.textContent = this.t(key, params);
            }
        });

        // Update language switch button text
        const langSwitch = document.querySelector('.lang-switch');
        if (langSwitch) {
            langSwitch.textContent = this.t('common.language_switch');
        }
    }

    toggleLanguage() {
        this.currentLanguage = this.currentLanguage === 'en' ? 'zh' : 'en';
        localStorage.setItem('preferred-language', this.currentLanguage);
        this.applyTranslations();
    }

    formatBytes(bytes) {
        if (bytes === 0) return '0 B';
        
        const k = 1024;
        const sizes = this.currentLanguage === 'zh' ? 
            ['字节', 'KB', 'MB', 'GB', 'TB'] : 
            ['B', 'KB', 'MB', 'GB', 'TB'];
        
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        const size = parseFloat((bytes / Math.pow(k, i)).toFixed(1));
        
        return `${size} ${sizes[i]}`;
    }

    formatTime(date) {
        if (this.currentLanguage === 'zh') {
            return date.toLocaleString('zh-CN');
        } else {
            return date.toLocaleString('en-US');
        }
    }

    formatDuration(seconds) {
        if (seconds < 60) {
            return this.currentLanguage === 'zh' ? `${seconds}秒` : `${seconds}s`;
        } else if (seconds < 3600) {
            const minutes = Math.floor(seconds / 60);
            return this.currentLanguage === 'zh' ? `${minutes}分钟` : `${minutes}m`;
        } else if (seconds < 86400) {
            const hours = Math.floor(seconds / 3600);
            return this.currentLanguage === 'zh' ? `${hours}小时` : `${hours}h`;
        } else {
            const days = Math.floor(seconds / 86400);
            return this.currentLanguage === 'zh' ? `${days}天` : `${days}d`;
        }
    }
}

// Initialize i18n when DOM is ready
window.addEventListener('DOMContentLoaded', function() {
    window.i18n = new I18n();
});

// Export for module usage
if (typeof module !== 'undefined' && module.exports) {
    module.exports = I18n;
} 