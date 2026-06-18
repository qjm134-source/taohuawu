// 玩家类
class Player extends Phaser.GameObjects.Container {
    constructor(scene, x, y, options = {}) {
        super(scene, x, y);

        this.scene = scene;
        this.scene.add.existing(this);

        // 配置
        this.speed = options.speed || 200;
        this.charScale = options.scale || 1;

        const s = this.charScale;

        // 创建纹理
        const bodyKey    = createEllipseTexture(scene, 35 * s, 70 * s, 0x4169E1, 1, 2, 0x000000);
        const headKey    = createCircleTexture(scene, 22 * s, 0xFFE0BD, 1, 2, 0x000000);
        const eyeKey     = createCircleTexture(scene, 3 * s, 0x000000);
        const mouthKey   = createEllipseTexture(scene, 10 * s, 5 * s, 0xFF6B6B, 1, 1.5, 0x000000);
        const shadowKey  = createEllipseTexture(scene, 45 * s, 8 * s, 0x000000, 0.2);

        // 创建身体部件（使用 Image 替代 Shape）
        this.body = scene.add.image(0, 0, bodyKey).setOrigin(0.5, 0.5);
        this.head = scene.add.image(0, -40 * s, headKey).setOrigin(0.5, 0.5);
        this.leftEye = scene.add.image(-7 * s, -43 * s, eyeKey).setOrigin(0.5, 0.5);
        this.rightEye = scene.add.image(7 * s, -43 * s, eyeKey).setOrigin(0.5, 0.5);
        this.mouth = scene.add.image(0, -33 * s, mouthKey).setOrigin(0.5, 0.5);
        this.shadow = scene.add.image(0, 45 * s, shadowKey).setOrigin(0.5, 0.5);

        // 添加所有元素到容器
        this.add([this.shadow, this.body, this.head, this.leftEye, this.rightEye, this.mouth]);

        // 移动状态
        this.isMoving = false;
        this.targetX = x;
        this.targetY = y;

        // 键盘控制
        this.cursors = scene.input.keyboard.createCursorKeys();
        this.wasd = scene.input.keyboard.addKeys({
            w: Phaser.Input.Keyboard.KeyCodes.W,
            a: Phaser.Input.Keyboard.KeyCodes.A,
            s: Phaser.Input.Keyboard.KeyCodes.S,
            d: Phaser.Input.Keyboard.KeyCodes.D,
        });
    }

    update(delta) {
        // 处理键盘输入
        let vx = 0;
        let vy = 0;

        if (this.cursors.left.isDown || this.wasd.a.isDown) {
            vx = -this.speed;
        } else if (this.cursors.right.isDown || this.wasd.d.isDown) {
            vx = this.speed;
        }

        if (this.cursors.up.isDown || this.wasd.w.isDown) {
            vy = -this.speed;
        } else if (this.cursors.down.isDown || this.wasd.s.isDown) {
            vy = this.speed;
        }

        // 应用速度
        if (vx !== 0 || vy !== 0) {
            this.isMoving = true;

            // 更新位置
            this.x += vx * (delta / 1000);
            this.y += vy * (delta / 1000);

            // 限制在场景边界内
            this.clampPosition();
        } else {
            this.isMoving = false;
        }
    }

    clampPosition() {
        const margin = 50;
        this.x = Phaser.Math.Clamp(
            this.x,
            margin,
            GAME_CONFIG.width - margin
        );
        this.y = Phaser.Math.Clamp(
            this.y,
            margin,
            GAME_CONFIG.height - margin
        );
    }

    moveTo(x, y) {
        this.targetX = x;
        this.targetY = y;

        // 计算距离和角度
        const dx = x - this.x;
        const dy = y - this.y;
        const distance = Math.sqrt(dx * dx + dy * dy);

        if (distance > 0) {
            const duration = (distance / this.speed) * 1000;

            this.scene.tweens.add({
                targets: this,
                x: x,
                y: y,
                duration: duration,
                ease: 'Linear',
                onUpdate: () => {
                    this.isMoving = true;
                },
                onComplete: () => {
                    this.isMoving = false;
                },
            });
        }
    }

    destroy() {
        super.destroy();
    }
}