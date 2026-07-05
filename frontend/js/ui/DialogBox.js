// 对话框组件
class DialogBox extends Phaser.GameObjects.Container {
    constructor(scene, x, y, options = {}) {
        super(scene, x, y);

        this.scene = scene;
        this.scene.add.existing(this);

        // 配置
        this.width = options.width || 800;
        this.height = options.height || 150;
        this.padding = options.padding || 20;
        this.borderColor = options.borderColor || 0x4A4A4A;
        this.backgroundColor = options.backgroundColor || 0xFFFFFF;
        this.borderColor = 0x4A4A4A;

        // 创建背景
        this.background = scene.add.graphics();
        this.background.fillStyle(0xFFFFFF, 0.95);
        this.background.lineStyle(3, this.borderColor);
        this.background.fillRect(-this.width / 2, -this.height / 2, this.width, this.height);
        this.background.strokeRect(-this.width / 2, -this.height / 2, this.width, this.height);

        // 创建 NPC 名称标签
        this.nameLabel = scene.add.text(
            -this.width / 2 + this.padding,
            -this.height / 2 + this.padding,
            '',
            {
                fontFamily: 'Microsoft YaHei',
                fontSize: '20px',
                fontStyle: 'bold',
                color: '#FF69B4',
            }
        );
        this.nameLabel.setOrigin(0, 0);

        // 创建对话框文本
        this.dialogText = scene.add.text(
            -this.width / 2 + this.padding,
            -this.height / 2 + this.padding + 30,
            '',
            {
                fontFamily: 'Microsoft YaHei',
                fontSize: '16px',
                color: '#333333',
                wordWrap: { width: this.width - this.padding * 2, useAdvancedWrap: true },
                align: 'left',
                lineSpacing: 5,
            }
        );
        this.dialogText.setOrigin(0, 0);

        // 创建情绪表情
        this.emojiLabel = scene.add.text(
            this.width / 2 - this.padding,
            -this.height / 2 + this.padding,
            '😊',
            {
                fontFamily: 'Segoe UI Emoji',
                fontSize: '24px',
            }
        );
        this.emojiLabel.setOrigin(1, 0);

        // 添加所有元素到容器
        this.add([this.background, this.nameLabel, this.dialogText, this.emojiLabel]);

        // 状态
        this.isVisible = false;
        this.currentText = '';
        this.typewriter = null;

        // 点击跳过
        this.setInteractive(new Phaser.Geom.Rectangle(
            -this.width / 2, -this.height / 2,
            this.width, this.height
        ), Phaser.Geom.Rectangle.Contains);

        this.on('pointerdown', () => {
            if (this.typewriter && this.typewriter.isTyping()) {
                this.typewriter.skip();
            }
        });

        // 初始隐藏
        this.setVisible(false);
    }

    show(npcName, text, emotion = 'neutral') {
        this.nameLabel.setText(npcName);
        this.currentText = text;

        // 设置表情
        this.emojiLabel.setText(EMOJIS[emotion] || EMOJIS.neutral);

        this.setVisible(true);
        this.isVisible = true;

        // 创建打字机效果
        if (this.typewriter) {
            this.typewriter.destroy();
        }

        this.typewriter = new Typewriter(this.scene, {
            x: -this.width / 2 + this.padding,
            y: -this.height / 2 + this.padding + 30,
            text: text,
            style: {
                fontFamily: 'Microsoft YaHei',
                fontSize: '16px',
                color: '#333333',
                wordWrap: { width: this.width - this.padding * 2, useAdvancedWrap: true },
                align: 'left',
                lineSpacing: 5,
            },
            speed: 30,
        });

        this.typewriter.start();
    }

    hide() {
        if (this.typewriter) {
            this.typewriter.destroy();
            this.typewriter = null;
        }
        this.setVisible(false);
        this.isVisible = false;
    }

    updateText(text) {
        this.currentText = text;
        if (this.typewriter) {
            this.typewriter.start(text);
        }
    }

    append(text) {
        if (!this.isVisible) {
            this.show('小荷', text, 'happy');
            return;
        }

        this.currentText += text;
        if (this.typewriter) {
            this.typewriter.append(text);
        }
    }

    updateEmotion(emotion) {
        this.emojiLabel.setText(EMOJIS[emotion] || EMOJIS.neutral);
    }

    isDisplaying() {
        return this.isVisible;
    }

    destroy() {
        if (this.typewriter) {
            this.typewriter.destroy();
        }
        super.destroy();
    }
}