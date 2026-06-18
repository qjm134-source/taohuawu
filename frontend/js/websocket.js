/**
 * WebSocket 客户端模块
 * 负责与后端 Go + Gin WebSocket 服务通信
 */
const WSClient = (() => {
    // 配置
    const CONFIG = {
        url: 'ws://localhost:8080/ws/game',
        reconnectInterval: 3000,
        maxReconnectInterval: 30000,
        pingInterval: 30000,
    };

    // 消息类型常量
    const MSG_TYPE = {
        CONNECTION: 'CONNECTION',
        CHAT_MESSAGE: 'CHAT_MESSAGE',
        PING: 'PING',
        WELCOME: 'WELCOME',
        NPC_REPLY: 'NPC_REPLY',
        ERROR: 'ERROR',
        PONG: 'PONG',
    };

    // 内部状态
    let socket = null;
    let isConnected = false;
    let messageQueue = [];
    let pingTimer = null;
    let reconnectTimer = null;
    let reconnectAttempts = 0;
    let requestId = 0;
    let playerId = '';
    let deviceId = '';

    // 事件回调
    let onMessageCallback = null;
    let onConnectCallback = null;
    let onDisconnectCallback = null;
    let onErrorCallback = null;

    /**
     * 生成请求 ID
     */
    function getRequestId() {
        return `req_${Date.now()}_${requestId++}`;
    }

    /**
     * 生成设备 ID
     */
    function generateDeviceId() {
        const stored = localStorage.getItem('ws_device_id');
        if (stored) return stored;
        const id = 'device_' + Date.now() + '_' + Math.random().toString(36).substring(2, 9);
        localStorage.setItem('ws_device_id', id);
        return id;
    }

    /**
     * 生成玩家 ID
     */
    function generatePlayerId() {
        const stored = localStorage.getItem('ws_player_id');
        if (stored) return stored;
        const id = 'player_' + Date.now() + '_' + Math.random().toString(36).substring(2, 9);
        localStorage.setItem('ws_player_id', id);
        return id;
    }

    /**
     * 连接 WebSocket
     */
    function connect() {
        if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
            return;
        }

        try {
            socket = new WebSocket(CONFIG.url);

            socket.onopen = () => {
                console.log('[WS] Connected');
                isConnected = true;
                reconnectAttempts = 0;
                startPing();
                flushMessageQueue();

                // 发送连接消息
                playerId = generatePlayerId();
                deviceId = generateDeviceId();
                sendConnection(playerId, '游客', deviceId);

                if (onConnectCallback) onConnectCallback();
            };

            socket.onmessage = (event) => {
                try {
                    const message = JSON.parse(event.data);

                    // 忽略 PONG 消息
                    if (message.type === MSG_TYPE.PONG) return;

                    if (onMessageCallback) {
                        onMessageCallback(message);
                    }
                } catch (err) {
                    console.error('[WS] Failed to parse message:', err);
                }
            };

            socket.onerror = (error) => {
                console.error('[WS] Error:', error);
                if (onErrorCallback) onErrorCallback(error);
            };

            socket.onclose = (event) => {
                console.log('[WS] Disconnected:', event.code, event.reason);
                isConnected = false;
                stopPing();
                scheduleReconnect();

                if (onDisconnectCallback) onDisconnectCallback();
            };
        } catch (err) {
            console.error('[WS] Failed to connect:', err);
            scheduleReconnect();
        }
    }

    /**
     * 断开连接
     */
    function disconnect() {
        stopPing();
        stopReconnect();
        if (socket) {
            socket.close(1000, 'Client disconnect');
            socket = null;
        }
        isConnected = false;
    }

    /**
     * 发送消息
     */
    function send(message) {
        if (!isConnected || !socket || socket.readyState !== WebSocket.OPEN) {
            messageQueue.push(message);
            console.log('[WS] Message queued (not connected)');
            return false;
        }

        try {
            socket.send(JSON.stringify(message));
            return true;
        } catch (err) {
            console.error('[WS] Failed to send:', err);
            messageQueue.push(message);
            return false;
        }
    }

    /**
     * 发送连接消息
     */
    function sendConnection(pid, nickname, did) {
        send({
            type: MSG_TYPE.CONNECTION,
            requestId: getRequestId(),
            tenantId: 'tenant_001',
            timestamp: Date.now(),
            payload: {
                playerId: pid,
                nickname: nickname,
                deviceId: did,
            },
        });
    }

    /**
     * 发送聊天消息
     */
    function sendChatMessage(message) {
        return send({
            type: MSG_TYPE.CHAT_MESSAGE,
            requestId: getRequestId(),
            tenantId: 'tenant_001',
            timestamp: Date.now(),
            payload: {
                message: message,
                playerId: playerId,
            },
        });
    }

    /**
     * 发送心跳
     */
    function sendPing() {
        send({
            type: MSG_TYPE.PING,
            requestId: getRequestId(),
            tenantId: 'tenant_001',
            timestamp: Date.now(),
            payload: {},
        });
    }

    /**
     * 清空消息队列
     */
    function flushMessageQueue() {
        while (messageQueue.length > 0) {
            const msg = messageQueue.shift();
            send(msg);
        }
    }

    /**
     * 启动心跳
     */
    function startPing() {
        stopPing();
        pingTimer = setInterval(() => {
            sendPing();
        }, CONFIG.pingInterval);
    }

    /**
     * 停止心跳
     */
    function stopPing() {
        if (pingTimer) {
            clearInterval(pingTimer);
            pingTimer = null;
        }
    }

    /**
     * 计划重连（指数退避）
     */
    function scheduleReconnect() {
        stopReconnect();
        const delay = Math.min(
            CONFIG.reconnectInterval * Math.pow(1.5, reconnectAttempts),
            CONFIG.maxReconnectInterval
        );
        reconnectAttempts++;
        console.log(`[WS] Reconnecting in ${delay}ms (attempt ${reconnectAttempts})`);

        reconnectTimer = setTimeout(() => {
            connect();
        }, delay);
    }

    /**
     * 停止重连
     */
    function stopReconnect() {
        if (reconnectTimer) {
            clearTimeout(reconnectTimer);
            reconnectTimer = null;
        }
    }

    /**
     * 获取连接状态
     */
    function getConnected() {
        return isConnected;
    }

    /**
     * 获取玩家 ID
     */
    function getPlayerId() {
        return playerId;
    }

    // 公共 API
    return {
        MSG_TYPE,
        connect,
        disconnect,
        sendChatMessage,
        getConnected,
        getPlayerId,

        get onMessage() { return onMessageCallback; },
        set onMessage(fn) { onMessageCallback = fn; },

        get onConnect() { return onConnectCallback; },
        set onConnect(fn) { onConnectCallback = fn; },

        get onDisconnect() { return onDisconnectCallback; },
        set onDisconnect(fn) { onDisconnectCallback = fn; },

        get onError() { return onErrorCallback; },
        set onError(fn) { onErrorCallback = fn; },
    };
})();