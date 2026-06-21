/**
 * UI 交互模块
 * 负责所有 UI 元素的更新和交互逻辑
 */
const UI = (() => {
    // DOM 元素缓存
    let elements = {};

    // 对话历史
    let chatHistory = [];

    /**
     * 初始化 UI，缓存所有 DOM 元素
     */
    function init() {
        elements = {
            welcomeArea: document.getElementById('welcomeArea'),
            welcomeText: document.getElementById('welcomeText'),
            welcomeCursor: document.getElementById('welcomeCursor'),
            guideArea: document.getElementById('guideArea'),
            speechBubble: document.getElementById('speechBubble'),
            bubbleContent: document.getElementById('bubbleContent'),
            chatInput: document.getElementById('chatInput'),
            sendBtn: document.getElementById('sendBtn'),
            inputArea: document.getElementById('inputArea'),
            connectionStatus: document.getElementById('connectionStatus'),
            statusDot: document.querySelector('.status-dot'),
            statusText: document.querySelector('.status-text'),
            historyBtn: document.getElementById('historyBtn'),
            historyOverlay: document.getElementById('historyOverlay'),
            historySidebar: document.getElementById('historySidebar'),
            historyClose: document.getElementById('historyClose'),
            historyList: document.getElementById('historyList'),
        };

        // 加载本地历史
        loadHistory();
    }

    /**
     * 更新连接状态
     * @param {'connected'|'disconnected'|'connecting'} status
     */
    function setConnectionStatus(status) {
        const el = elements.connectionStatus;
        if (!el) return;

        el.classList.remove('connected', 'disconnected');

        switch (status) {
            case 'connected':
                el.classList.add('connected');
                elements.statusText.textContent = '已连接';
                break;
            case 'disconnected':
                el.classList.add('disconnected');
                elements.statusText.textContent = '已断开';
                break;
            default:
                elements.statusText.textContent = '连接中...';
                break;
        }
    }

    /**
     * 显示欢迎词（通过打字机效果）
     * @param {string} text - 欢迎文字
     * @param {Function} typewriterStart - 打字机启动函数
     */
    function showWelcome(text, typewriterStart) {
        if (!elements.welcomeText) return;
        typewriterStart(text);
    }

    /**
     * 更新导游气泡
     * @param {string} message - 气泡内容
     * @param {boolean} isThinking - 是否为"思考中"状态
     */
    function updateBubble(message, isThinking = false) {
        const bubble = elements.speechBubble;
        const content = elements.bubbleContent;
        if (!bubble || !content) return;

        if (isThinking) {
            content.innerHTML = '<span class="bubble-thinking">小荷正在思考...</span>';
            bubble.classList.add('visible');
        } else {
            content.textContent = message;
            // 触发气泡动画：先移除再添加
            bubble.classList.remove('visible');
            // 强制回流
            void bubble.offsetWidth;
            bubble.classList.add('visible');
        }
    }

    /**
     * 隐藏气泡
     */
    function hideBubble() {
        const bubble = elements.speechBubble;
        if (bubble) {
            bubble.classList.remove('visible');
        }
    }

    /**
     * 获取输入框内容并清空
     * @returns {string} 输入的文字
     */
    function getInputAndClear() {
        const input = elements.chatInput;
        if (!input) return '';
        const text = input.value.trim();
        input.value = '';
        // 重置高度
        input.style.height = 'auto';
        return text;
    }

    /**
     * 设置输入框是否可用
     */
    function setInputEnabled(enabled) {
        const input = elements.chatInput;
        const btn = elements.sendBtn;
        if (input) input.disabled = !enabled;
        if (btn) btn.disabled = !enabled;
    }

    /**
     * 聚焦输入框
     */
    function focusInput() {
        if (elements.chatInput) {
            elements.chatInput.focus();
        }
    }

    /**
     * 添加对话历史记录
     */
    function addToHistory(userMessage, replyMessage) {
        const item = {
            user: userMessage,
            reply: replyMessage,
            time: new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }),
        };
        chatHistory.push(item);

        // 限制历史数量
        if (chatHistory.length > 100) {
            chatHistory.shift();
        }

        // 保存到本地
        saveHistory();

        // 更新侧边栏
        renderHistoryItem(item);
    }

    /**
     * 渲染一条历史记录到侧边栏
     */
    function renderHistoryItem(item) {
        const list = elements.historyList;
        if (!list) return;

        // 移除空状态
        const empty = list.querySelector('.history-empty');
        if (empty) empty.remove();

        const div = document.createElement('div');
        div.className = 'history-item';
        div.innerHTML = `
            <div class="history-item-user">你：${escapeHtml(item.user)}</div>
            <div class="history-item-reply">小荷：${escapeHtml(item.reply)}</div>
            <div class="history-item-time">${item.time}</div>
        `;

        // 插入到最前面
        list.insertBefore(div, list.firstChild);
    }

    /**
     * 切换历史侧边栏
     */
    function toggleHistory() {
        const overlay = elements.historyOverlay;
        const sidebar = elements.historySidebar;

        if (!overlay || !sidebar) return;

        const isVisible = sidebar.classList.contains('visible');

        if (isVisible) {
            overlay.classList.remove('visible');
            sidebar.classList.remove('visible');
        } else {
            overlay.classList.add('visible');
            sidebar.classList.add('visible');
        }
    }

    /**
     * 关闭历史侧边栏
     */
    function closeHistory() {
        const overlay = elements.historyOverlay;
        const sidebar = elements.historySidebar;
        if (overlay) overlay.classList.remove('visible');
        if (sidebar) sidebar.classList.remove('visible');
    }

    /**
     * 保存对话历史到 localStorage
     */
    function saveHistory() {
        try {
            // 只保存最近 50 条
            const toSave = chatHistory.slice(-50);
            localStorage.setItem('chat_history', JSON.stringify(toSave));
        } catch (e) {
            // localStorage 可能已满或不可用
        }
    }

    /**
     * 从 localStorage 加载对话历史
     */
    function loadHistory() {
        try {
            const saved = localStorage.getItem('chat_history');
            if (saved) {
                chatHistory = JSON.parse(saved);
                // 渲染到侧边栏
                chatHistory.slice().reverse().forEach(item => {
                    renderHistoryItem(item);
                });
            }
        } catch (e) {
            chatHistory = [];
        }
    }

    /**
     * HTML 转义
     */
    function escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    /**
     * 自动调整输入框高度
     */
    function autoResizeInput() {
        const input = elements.chatInput;
        if (!input) return;

        input.style.height = 'auto';
        input.style.height = Math.min(input.scrollHeight, 120) + 'px';
    }

    /**
     * 更新调试面板
     * @param {Object} stats - LLM 统计信息
     * @param {string} stats.model - 模型名称
     * @param {number} stats.latencyMs - 耗时(毫秒)
     * @param {number} stats.inputTokens - 输入 token 数
     * @param {number} stats.outputTokens - 输出 token 数
     * @param {number} stats.totalTokens - 总 token 数
     * @param {number} stats.cost - 费用(元)
     */
    function updateDebugPanel(stats) {
        if (!stats) return;

        var modelEl = document.getElementById('debugModel');
        var latencyEl = document.getElementById('debugLatency');
        var tokensEl = document.getElementById('debugTokens');
        var costEl = document.getElementById('debugCost');

        if (modelEl) modelEl.textContent = stats.model || '—';
        if (latencyEl) latencyEl.textContent = stats.latencyMs ? (stats.latencyMs / 1000).toFixed(1) + 's' : '—';
        if (tokensEl) {
            var input = stats.inputTokens || 0;
            var output = stats.outputTokens || 0;
            var total = stats.totalTokens || (input + output);
            tokensEl.textContent = total + ' (输入 ' + input + ' + 输出 ' + output + ')';
        }
        if (costEl) costEl.textContent = stats.cost != null ? '¥' + stats.cost.toFixed(3) : '—';

        // 自动展开调试面板
        var content = document.getElementById('debugContent');
        var toggle = document.getElementById('debugToggle');
        if (content && !content.classList.contains('visible')) {
            content.classList.add('visible');
            if (toggle) toggle.classList.add('active');
        }
    }

    /**
     * 切换调试面板显示/隐藏
     */
    function toggleDebugPanel() {
        var content = document.getElementById('debugContent');
        var toggle = document.getElementById('debugToggle');
        if (!content || !toggle) return;

        var isVisible = content.classList.contains('visible');
        if (isVisible) {
            content.classList.remove('visible');
            toggle.classList.remove('active');
        } else {
            content.classList.add('visible');
            toggle.classList.add('active');
        }
    }

    // 公共 API
    return {
        init,
        setConnectionStatus,
        showWelcome,
        updateBubble,
        hideBubble,
        getInputAndClear,
        setInputEnabled,
        focusInput,
        addToHistory,
        toggleHistory,
        closeHistory,
        autoResizeInput,
        updateDebugPanel,
        toggleDebugPanel,
    };
})();