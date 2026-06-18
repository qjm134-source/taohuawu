/**
 * 江南水乡 - 智能导游系统
 * 主入口模块，负责初始化和连接各模块
 */
(function () {
    'use strict';

    // ========== 打字机实例 ==========
    const welcomeTypewriter = Typewriter.create(
        document.getElementById('welcomeText'),
        document.getElementById('welcomeCursor'),
        {
            speed: 80,
            startDelay: 500,
            onComplete: () => {
                // 打字完成后光标继续闪烁
                console.log('[App] Welcome typewriter complete');
            },
        }
    );

    // ========== 初始化 UI ==========
    UI.init();

    // ========== WebSocket 事件处理 ==========

    /**
     * 处理服务器消息
     */
    WSClient.onMessage = (message) => {
        console.log('[App] Received:', message.type);

        switch (message.type) {
            case WSClient.MSG_TYPE.WELCOME:
                handleWelcome(message);
                break;

            case WSClient.MSG_TYPE.NPC_REPLY:
                handleNPCReply(message);
                break;

            case WSClient.MSG_TYPE.ERROR:
                handleError(message);
                break;

            default:
                console.log('[App] Unknown message type:', message.type);
        }
    };

    /**
     * 连接成功
     */
    WSClient.onConnect = () => {
        console.log('[App] WebSocket connected');
        UI.setConnectionStatus('connected');
        UI.setInputEnabled(true);
    };

    /**
     * 连接断开
     */
    WSClient.onDisconnect = () => {
        console.log('[App] WebSocket disconnected');
        UI.setConnectionStatus('disconnected');
        UI.setInputEnabled(false);
    };

    /**
     * 连接错误
     */
    WSClient.onError = (error) => {
        console.error('[App] WebSocket error:', error);
        UI.setConnectionStatus('disconnected');
    };

    // ========== 消息处理器 ==========

    /**
     * 处理欢迎消息
     */
    function handleWelcome(message) {
        try {
            const payload = message.payload;
            const welcomeMsg = payload.message || '欢迎来到江南水乡！';

            console.log('[App] Welcome:', welcomeMsg);

            // 打字机显示欢迎词
            UI.showWelcome(welcomeMsg, (text) => {
                welcomeTypewriter.start(text);
            });

            // 如果消息中包含 tips，也可以在气泡中显示
            if (payload.tips && payload.tips.length > 0) {
                setTimeout(() => {
                    UI.updateBubble(payload.tips[0]);
                }, welcomeMsg.length * 80 + 1000); // 等打字机结束后显示
            }
        } catch (err) {
            console.error('[App] Failed to handle welcome:', err);
        }
    }

    /**
     * 处理 NPC 回复
     */
    function handleNPCReply(message) {
        try {
            const payload = message.payload;
            const reply = payload.message || '';

            console.log('[App] NPC reply:', reply);

            // 更新气泡显示回复
            UI.updateBubble(reply);
        } catch (err) {
            console.error('[App] Failed to handle NPC reply:', err);
        }
    }

    /**
     * 处理错误消息
     */
    function handleError(message) {
        try {
            const payload = message.payload;
            const errorMsg = payload.message || '发生了未知错误';

            console.error('[App] Server error:', errorMsg);

            // 在气泡中显示错误
            UI.updateBubble('抱歉，' + errorMsg);
        } catch (err) {
            console.error('[App] Failed to handle error:', err);
        }
    }

    // ========== 用户交互 ==========

    /**
     * 发送消息
     */
    function sendMessage() {
        const text = UI.getInputAndClear();
        if (!text) return;

        if (!WSClient.getConnected()) {
            UI.updateBubble('连接已断开，正在尝试重连...');
            return;
        }

        // 显示思考状态
        UI.updateBubble(null, true);

        // 发送到服务器
        const sent = WSClient.sendChatMessage(text);

        if (sent) {
            // 暂存用户消息，等收到回复后一起加入历史
            window._lastUserMessage = text;
        } else {
            UI.updateBubble('消息发送失败，请稍后再试');
        }
    }

    // 包装 handleNPCReply 以保存对话历史
    const originalOnMessage = WSClient.onMessage;
    WSClient.onMessage = (message) => {
        // 先调用原始处理器
        if (message.type === WSClient.MSG_TYPE.NPC_REPLY) {
            const userMsg = window._lastUserMessage;
            if (userMsg) {
                try {
                    const payload = message.payload;
                    UI.addToHistory(userMsg, payload.message || '');
                } catch (e) {
                    // ignore
                }
                window._lastUserMessage = null;
            }
        }

        // 调用原始处理器
        if (originalOnMessage) {
            originalOnMessage(message);
        }
    };

    /**
     * 处理输入框按键
     */
    function handleInputKeydown(e) {
        // Enter 发送（Shift+Enter 换行）
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            sendMessage();
        }
    }

    /**
     * 处理输入框输入（自动调整高度）
     */
    function handleInputInput() {
        UI.autoResizeInput();
    }

    // ========== 事件绑定 ==========

    function bindEvents() {
        // 发送按钮
        const sendBtn = document.getElementById('sendBtn');
        if (sendBtn) {
            sendBtn.addEventListener('click', sendMessage);
        }

        // 输入框
        const chatInput = document.getElementById('chatInput');
        if (chatInput) {
            chatInput.addEventListener('keydown', handleInputKeydown);
            chatInput.addEventListener('input', handleInputInput);
            chatInput.addEventListener('focus', () => {
                document.getElementById('inputArea')?.classList.add('focused');
            });
            chatInput.addEventListener('blur', () => {
                document.getElementById('inputArea')?.classList.remove('focused');
            });
        }

        // 历史按钮
        const historyBtn = document.getElementById('historyBtn');
        if (historyBtn) {
            historyBtn.addEventListener('click', UI.toggleHistory);
        }

        // 关闭历史
        const historyClose = document.getElementById('historyClose');
        if (historyClose) {
            historyClose.addEventListener('click', UI.closeHistory);
        }

        // 点击遮罩关闭历史
        const historyOverlay = document.getElementById('historyOverlay');
        if (historyOverlay) {
            historyOverlay.addEventListener('click', UI.closeHistory);
        }

        // ESC 关闭历史
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                UI.closeHistory();
            }
        });

        // 点击页面任意位置（除输入框外）聚焦输入框
        document.addEventListener('click', (e) => {
            const target = e.target;
            // 不响应按钮、链接、输入框、侧边栏的点击
            if (
                target.closest('button') ||
                target.closest('a') ||
                target.closest('textarea') ||
                target.closest('input') ||
                target.closest('.history-sidebar') ||
                target.closest('.history-overlay')
            ) {
                return;
            }
            UI.focusInput();
        });
    }

    // ========== 启动 ==========

    function boot() {
        console.log('[App] Booting...');

        // 绑定事件
        bindEvents();

        // 初始状态：输入框禁用
        UI.setInputEnabled(false);
        UI.setConnectionStatus('connecting');

        // 连接 WebSocket
        WSClient.connect();

        // 初始显示气泡
        UI.updateBubble('正在连接水乡...', true);

        console.log('[App] Boot complete');
    }

    // DOM 加载完成后启动
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', boot);
    } else {
        boot();
    }
})();