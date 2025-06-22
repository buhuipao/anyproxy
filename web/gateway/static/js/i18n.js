// Internationalization (i18n) Support for AnyProxy Gateway
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

                // Dashboard
                'dashboard.title': 'AnyProxy Gateway Dashboard',
                'dashboard.subtitle': 'Gateway Management Interface - Real-time Monitoring & Configuration',
                'dashboard.welcome': 'Welcome, ',
                'dashboard.logout': 'Logout',

                // Metrics
                'metrics.active_connections': 'Active Connections',
                'metrics.total_connections': 'Total Connections',
                'metrics.bytes_sent': 'Data Sent',
                'metrics.bytes_received': 'Data Received',
                'metrics.success_rate': 'Success Rate',
                'metrics.uptime': 'Uptime',
                'metrics.error_count': 'Error Count',

                // Clients
                'clients.title': 'Client Status',
                'clients.client_id': 'Client ID',
                'clients.active_connections': 'Active Connections',
                'clients.data_sent': 'Data Sent',
                'clients.data_received': 'Data Received',
                'clients.status': 'Status',
                'clients.no_clients': 'No connected clients',
                'clients.no_online_clients': 'No online clients',
                'clients.show_offline': 'Show Offline Clients',

                // Login
                'login.title': 'AnyProxy Gateway - Login',
                'login.welcome': 'AnyProxy',
                'login.subtitle': 'Gateway Management System',
                'login.username': 'Username',
                'login.password': 'Password',
                'login.button': 'Login',
                'login.logging_in': 'Logging in...',
                'login.error.invalid': 'Invalid username or password',
                'login.error.network': 'Network error, please try again later',
                'login.footer': '© 2025 AnyProxy. Authentication required to access management interface.',

                // Navigation
                'nav.dashboard': 'Dashboard',
                'nav.clients': 'Clients',
                'nav.metrics': 'Metrics',
                'nav.rate_limit': 'Rate Limiting',
                'nav.settings': 'Settings',

                // Language
                'lang.switch': 'Language',
                'lang.en': 'English',
                'lang.zh': '中文',

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

                // Dashboard
                'dashboard.title': 'AnyProxy 网关仪表盘',
                'dashboard.subtitle': '网关管理界面 - 实时监控与配置',
                'dashboard.welcome': '欢迎，',
                'dashboard.logout': '登出',

                // Metrics
                'metrics.active_connections': '活跃连接',
                'metrics.total_connections': '总连接数',
                'metrics.bytes_sent': '发送数据',
                'metrics.bytes_received': '接收数据',
                'metrics.success_rate': '成功率',
                'metrics.uptime': '运行时间',
                'metrics.error_count': '错误计数',

                // Clients
                'clients.title': '客户端状态',
                'clients.client_id': '客户端ID',
                'clients.active_connections': '活跃连接',
                'clients.data_sent': '发送数据',
                'clients.data_received': '接收数据',
                'clients.status': '状态',
                'clients.no_clients': '没有客户端连接',
                'clients.no_online_clients': '没有在线客户端',
                'clients.show_offline': '显示离线客户端',

                // Login
                'login.title': 'AnyProxy 网关 - 登录',
                'login.welcome': 'AnyProxy',
                'login.subtitle': '网关管理系统',
                'login.username': '用户名',
                'login.password': '密码',
                'login.button': '登录',
                'login.logging_in': '登录中...',
                'login.error.invalid': '用户名或密码错误',
                'login.error.network': '网络错误，请稍后重试',
                'login.footer': '© 2025 AnyProxy. 访问管理界面需要认证。',

                // Navigation
                'nav.dashboard': '仪表盘',
                'nav.clients': '客户端',
                'nav.metrics': '指标',
                'nav.rate_limit': '限速',
                'nav.settings': '设置',

                // Language
                'lang.switch': '语言',
                'lang.en': 'English',
                'lang.zh': '中文',

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

    // Show error message with i18n support
    showError(message, isKey = false) {
        const text = isKey ? this.t(message) : message;
        // You can customize this to show errors in your preferred way
        alert(text);
    }

    // Show success message with i18n support
    showSuccess(message, isKey = false) {
        const text = isKey ? this.t(message) : message;
        // You can customize this to show success messages in your preferred way
        console.log('Success:', text);
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