// 江南水乡主场景
class WaterTownScene extends Phaser.Scene {
    constructor() {
        super({ key: 'WaterTownScene' });
    }

    create() {
        // 创建背景
        this.background = new Background(this);

        // 创建玩家
        this.player = new Player(this, 300, GAME_CONFIG.height * 0.7);

        // 创建 NPC 导游
        this.npc = new NPCGuide(this, NPC_CONFIG.guide.position.x, NPC_CONFIG.guide.position.y, {
            name: NPC_CONFIG.guide.name,
            scale: NPC_CONFIG.guide.scale,
            color: NPC_CONFIG.guide.color,
        });

        // 创建对话框
        this.dialogBox = new DialogBox(this, 0, GAME_CONFIG.height - 100, {
            width: 800,
            height: 150,
        });
        this.dialogBox.setVisible(false);

        // 创建输入框
        this.inputBox = new InputBox(this, 0, GAME_CONFIG.height - 250, {
            width: 600,
            height: 50,
            placeholder: '输入问题向导游小荷提问...',
            onSend: this.handleInput.bind(this),
        });
        this.inputBox.setVisible(false);

        // WebSocket 连接
        this.wsClient = new WebSocketClient(WS_CONFIG.url);
        this.setupWebSocket();

        // 游戏状态
        this.playerId = null;
        this.nickname = '玩家';
        this.deviceId = this.wsClient.generateDeviceId();
        this.isConnected = false;

        // 连接状态提示
        this.connectionStatus = this.add.text(
            20,
            20,
            '连接中...',
            {
                fontFamily: 'Microsoft YaHei',
                fontSize: '14px',
                color: '#ffffff',
                backgroundColor: '#000000',
                padding: { x: 10, y: 5 },
            }
        );
    }

    setupWebSocket() {
        this.wsClient.onConnect = () => {
            this.isConnected = true;
            this.connectionStatus.setText('已连接').setBackgroundColor('#008000');

            // 发送连接消息
            this.playerId = this.wsClient.generatePlayerId();
            this.wsClient.sendConnection(this.playerId, this.nickname, this.deviceId);
        };

        this.wsClient.onDisconnect = () => {
            this.isConnected = false;
            this.connectionStatus.setText('连接断开').setBackgroundColor('#FF0000');
        };

        this.wsClient.onError = (error) => {
            this.connectionStatus.setText('连接错误').setBackgroundColor('#FF0000');
        };

        this.wsClient.onMessage = (message) => {
            this.handleMessage(message);
        };

        // 连接
        this.wsClient.connect();
    }

    handleMessage(message) {
        switch (message.type) {
            case MESSAGE_TYPES.WELCOME:
                this.handleWelcome(message);
                break;

            case MESSAGE_TYPES.NPC_REPLY:
                this.handleNPCReply(message);
                break;

            case MESSAGE_TYPES.ERROR:
                this.handleError(message);
                break;

            case MESSAGE_TYPES.PONG:
                // 心跳响应，无需处理
                break;

            case MESSAGE_TYPES.STREAM_EVENT:
                this.handleStreamEvent(message);
                break;

            default:
        }
    }

    handleWelcome(message) {
        const payload = message.payload;

        // 显示欢迎消息
        this.dialogBox.show(payload.guideName, payload.message, 'happy');

        // NPC 挥手
        this.npc.wave();

        // 显示输入框
        this.time.delayedCall(2000, () => {
            this.inputBox.show();
            this.inputBox.focus();
        });
    }

    handleNPCReply(message) {
        const payload = message.payload;

        // 显示回复
        this.dialogBox.show(payload.guideName, payload.message, payload.emotion);

        // 更新 NPC 情绪
        this.npc.setEmotion(payload.emotion);

        // 聚焦输入框
        this.inputBox.focus();
    }

    handleStreamEvent(message) {
        const payload = message.payload;

        if (payload.content && payload.content !== '') {
            this.dialogBox.append(payload.content);
        }

        if (payload.finishReason && payload.finishReason !== '') {
            this.inputBox.focus();
        }
    }

    handleError(message) {
        const payload = message.payload;

        // 显示错误消息
        this.dialogBox.show('系统', payload.message, 'confused');
    }

    handleInput(text) {
        if (!this.isConnected) {
            this.dialogBox.show('系统', '连接已断开，请稍后再试', 'confused');
            return;
        }

        if (!this.playerId) {
            this.dialogBox.show('系统', '正在连接中，请稍候', 'confused');
            return;
        }

        // 发送聊天消息
        this.wsClient.sendChatMessage(text, this.playerId);

        // NPC 转向玩家
        this.npc.setFlipX(this.player.x < this.npc.x);

        // NPC 点头
        this.tweens.add({
            targets: this.npc,
            rotation: -0.1,
            duration: 150,
            yoyo: true,
            repeat: 1,
        });
    }

    update(time, delta) {
        // 更新玩家
        if (this.player) {
            this.player.update(delta);
        }

        // 检查玩家与 NPC 的距离
        if (this.player && this.npc) {
            const distance = Phaser.Math.Distance.Between(
                this.player.x, this.player.y,
                this.npc.x, this.npc.y
            );

            // 距离太近时让 NPC 面向玩家
            if (distance < 200) {
                this.npc.setFlipX(this.player.x < this.npc.x);
            }
        }
    }

    shutdown() {
        if (this.wsClient) {
            this.wsClient.disconnect();
        }

        if (this.dialogBox) {
            this.dialogBox.destroy();
        }

        if (this.inputBox) {
            this.inputBox.destroy();
        }

        super.shutdown();
    }
}