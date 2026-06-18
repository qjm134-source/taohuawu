// NPC 导游类
class NPCGuide extends Phaser.GameObjects.Container {
    constructor(scene, x, y, options = {}) {
        super(scene, x, y);

        this.scene = scene;
        this.scene.add.existing(this);

        // 配置
        this.name = options.name || '小荷';
        this.charScale = options.scale || 1;
        this.color = options.color || 0xFF69B4;

        const s = this.charScale;

        // 创建纹理（白色填充用于需要变色部位，具体颜色用于不变色部位）
        const bodyKey     = createEllipseTexture(scene, 40 * s, 80 * s, 0xFFFFFF, 1, 2, 0x000000);
        const headKey     = createCircleTexture(scene, 25 * s, 0xFFE0BD, 1, 2, 0x000000);
        const eyeKey      = createCircleTexture(scene, 4 * s, 0x000000);
        const mouthKey    = createEllipseTexture(scene, 14 * s, 7 * s, 0xFFFFFF, 1, 1.5, 0x000000);
        const hairKey     = createEllipseTexture(scene, 15 * s, 20 * s, 0x8B4513);
        const hairTopKey  = createEllipseTexture(scene, 25 * s, 15 * s, 0x8B4513);
        const hairpinKey  = createPolygonTexture(scene, [0, -10, 8, 5, -8, 5], 0xFF69B4, 1, 2, 0x000000);
        const shadowKey   = createEllipseTexture(scene, 50 * s, 10 * s, 0x000000, 0.2);

        // 创建身体部件（使用 Image 替代 Shape，确保纹理在创建时就有效）
        this.body = scene.add.image(0, 0, bodyKey).setOrigin(0.5, 0.5).setTint(this.color);

        // 创建头部
        this.head = scene.add.image(0, -45 * s, headKey).setOrigin(0.5, 0.5);

        // 创建眼睛
        this.leftEye = scene.add.image(-8 * s, -48 * s, eyeKey).setOrigin(0.5, 0.5);
        this.rightEye = scene.add.image(8 * s, -48 * s, eyeKey).setOrigin(0.5, 0.5);

        // 创建嘴巴（白色底 + tint 变色）
        this.mouth = scene.add.image(0, -36 * s, mouthKey).setOrigin(0.5, 0.5).setTint(0xFF6B6B);

        // 创建头发
        this.hairLeft = scene.add.image(-15 * s, -55 * s, hairKey).setOrigin(0.5, 0.5);
        this.hairRight = scene.add.image(15 * s, -55 * s, hairKey).setOrigin(0.5, 0.5);
        this.hairTop = scene.add.image(0, -60 * s, hairTopKey).setOrigin(0.5, 0.5);

        // 创建发簪（荷花）
        this.hairpin = scene.add.image(0, -65 * s, hairpinKey).setOrigin(0.5, 0.5);

        // 创建阴影
        this.shadow = scene.add.image(0, 50 * s, shadowKey).setOrigin(0.5, 0.5);

        // 添加所有元素到容器
        this.add([
            this.shadow, this.body, this.head, this.leftEye, this.rightEye, this.mouth,
            this.hairLeft, this.hairRight, this.hairTop, this.hairpin,
        ]);

        // 设置可交互
        this.setSize(60 * this.charScale, 100 * this.charScale);
        this.setInteractive({ cursor: 'pointer' });

        // 状态
        this.isBreathing = true;
        this.breathTween = null;
        this.currentEmotion = 'neutral';
        this.facingLeft = false;

        // 启动呼吸动画
        this.startBreathing();
    }

    startBreathing() {
        if (this.breathTween) {
            this.breathTween.remove();
        }

        this.breathTween = this.scene.tweens.add({
            targets: this,
            scaleY: this.charScale * 1.02,
            yoyo: true,
            repeat: -1,
            duration: 2000,
            ease: 'Sine.easeInOut',
        });
    }

    stopBreathing() {
        if (this.breathTween) {
            this.breathTween.remove();
            this.breathTween = null;
        }
        this.scaleY = this.charScale;
    }

    setFlipX(flip) {
        this.facingLeft = flip;
        this.scaleX = flip ? -this.charScale : this.charScale;
    }

    setEmotion(emotion) {
        this.currentEmotion = emotion;

        // 根据情绪调整嘴巴颜色和身体色调
        switch (emotion) {
            case 'happy':
                this.updateMouth(0x4CAF50);
                break;
            case 'sad':
                this.updateMouth(0xFF6B6B);
                this.body.setTint(0x999999);
                break;
            case 'angry':
                this.updateMouth(0xFF4444);
                this.body.setTint(0xFF6666);
                break;
            case 'confused':
                this.updateMouth(0xFFA500);
                break;
            case 'excited':
                this.updateMouth(0xFFD700);
                this.body.setTint(0xFFFFAA);
                break;
            default:
                this.updateMouth(0xFF6B6B);
                this.body.clearTint();
        }
    }

    updateMouth(color) {
        this.mouth.setTint(color);
    }

    wave() {
        this.stopBreathing();

        // 挥手动画
        this.scene.tweens.add({
            targets: this.hairpin,
            rotation: -0.5,
            duration: 200,
            yoyo: true,
            repeat: 3,
            onComplete: () => {
                this.hairpin.rotation = 0;
                this.startBreathing();
            }
        });
    }

    jump() {
        this.stopBreathing();

        this.scene.tweens.add({
            targets: this,
            y: this.y - 30,
            duration: 200,
            ease: 'Quad.easeOut',
            yoyo: true,
            repeat: 0,
            onComplete: () => {
                this.startBreathing();
            }
        });
    }

    destroy() {
        this.stopBreathing();
        super.destroy();
    }
}
