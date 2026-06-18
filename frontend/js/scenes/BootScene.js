// 启动场景
class BootScene extends Phaser.Scene {
    constructor() {
        super({ key: 'BootScene' });
    }

    preload() {
        // 显示加载界面
        this.createLoadingBar();

        // 这里可以预加载资源
        // 由于我们使用的是程序化生成的图形，所以不需要加载外部资源
    }

    create() {
        // 启动主场景
        this.scene.start('WaterTownScene');
    }

    createLoadingBar() {
        const width = this.cameras.main.width;
        const height = this.cameras.main.height;

        // 背景
        this.add.rectangle(width / 2, height / 2, width, height, 0x2c3e50);

        // 加载文本
        this.add.text(
            width / 2,
            height / 2 - 50,
            '江南水乡',
            {
                fontFamily: 'Microsoft YaHei',
                fontSize: '48px',
                color: '#ffffff',
                fontStyle: 'bold',
            }
        ).setOrigin(0.5);

        // 加载提示
        this.add.text(
            width / 2,
            height / 2,
            '正在进入水乡...',
            {
                fontFamily: 'Microsoft YaHei',
                fontSize: '20px',
                color: '#ffffff',
            }
        ).setOrigin(0.5);

        // 加载条背景
        const barBg = this.add.graphics();
        barBg.fillStyle(0x34495e, 1);
        barBg.fillRect(width / 2 - 200, height / 2 + 30, 400, 20);

        // 加载条
        const bar = this.add.graphics();
        bar.fillStyle(0x3498db, 1);
        bar.fillRect(width / 2 - 200, height / 2 + 30, 400, 20);

        // 进度文字
        const progressText = this.add.text(
            width / 2,
            height / 2 + 70,
            '0%',
            {
                fontFamily: 'Microsoft YaHei',
                fontSize: '16px',
                color: '#ffffff',
            }
        ).setOrigin(0.5);

        // 模拟加载进度
        let progress = 0;
        let isDestroyed = false;
        const progressInterval = setInterval(() => {
            if (isDestroyed) return;
            
            progress += Math.random() * 10;
            if (progress >= 100) {
                progress = 100;
                isDestroyed = true;
                clearInterval(progressInterval);

                // 完成后过渡到下一个场景
                this.time.delayedCall(500, () => {
                    barBg.destroy();
                    bar.destroy();
                    progressText.destroy();
                });
            }

            bar.clear();
            bar.fillStyle(0x3498db, 1);
            bar.fillRect(width / 2 - 200, height / 2 + 30, 400 * (progress / 100), 20);

            progressText.setText(Math.floor(progress) + '%');
        }, 100);
    }
}