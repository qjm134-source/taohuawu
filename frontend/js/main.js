// 游戏主入口
class Game extends Phaser.Game {
    constructor() {
        const config = {
            type: Phaser.AUTO,
            parent: 'game-container',
            width: GAME_CONFIG.width,
            height: GAME_CONFIG.height,
            backgroundColor: GAME_CONFIG.backgroundColor,
            scale: {
                mode: Phaser.Scale.FIT,
                autoCenter: Phaser.Scale.CENTER_BOTH,
            },
            scene: [BootScene, WaterTownScene],
            physics: {
                default: 'arcade',
                arcade: {
                    gravity: { y: 0 },
                    debug: false,
                },
            },
        };

        super(config);
    }
}

// 等待页面加载完成
window.addEventListener('load', () => {
    // 隐藏加载界面
    const loading = document.getElementById('loading');
    if (loading) {
        loading.style.display = 'none';
    }

    // 启动游戏
    const game = new Game();
});