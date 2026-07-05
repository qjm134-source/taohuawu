// 打字机效果
class Typewriter {
    constructor(scene, options = {}) {
        this.scene = scene;
        this.text = options.text || '';
        this.x = options.x || 0;
        this.y = options.y || 0;
        this.style = options.style || {
            fontFamily: 'Microsoft YaHei',
            fontSize: '18px',
            color: '#333333',
            wordWrap: { width: 500, useAdvancedWrap: true },
        };
        this.speed = options.speed || 30; // 每个字符的间隔（毫秒）
        this.onComplete = options.onComplete || null;
        this.isPlaying = false;
        this.isPaused = false;
        this.isComplete = false;
        this.currentIndex = 0;
        this.timer = null;
        this.displayedText = '';

        // 创建文本对象
        this.textObject = scene.add.text(this.x, this.y, '', this.style);
        this.textObject.setOrigin(0.5);
    }

    start(text) {
        if (text) {
            this.text = text;
        }

        if (this.isPlaying) {
            this.stop();
        }

        this.isPlaying = true;
        this.isPaused = false;
        this.isComplete = false;
        this.currentIndex = 0;
        this.displayedText = '';
        this.textObject.setText('');

        this.typeNext();
    }

    typeNext() {
        if (this.isPaused || !this.isPlaying || this.isComplete) {
            return;
        }

        if (this.currentIndex < this.text.length) {
            this.displayedText += this.text[this.currentIndex];
            this.textObject.setText(this.displayedText);
            this.currentIndex++;

            this.timer = this.scene.time.delayedCall(this.speed, this.typeNext);
        } else {
            this.complete();
        }
    }

    pause() {
        this.isPaused = true;
        if (this.timer) {
            this.timer.remove();
        }
    }

    resume() {
        if (this.isPaused && !this.isComplete) {
            this.isPaused = false;
            this.typeNext();
        }
    }

    skip() {
        if (this.isPlaying && !this.isComplete) {
            this.stop();
            this.textObject.setText(this.text);
            this.isComplete = true;
            this.isPlaying = false;
            if (this.onComplete) {
                this.onComplete();
            }
        }
    }

    stop() {
        this.isPlaying = false;
        this.isPaused = false;
        if (this.timer) {
            this.timer.remove();
        }
    }

    complete() {
        this.isComplete = true;
        this.isPlaying = false;
        if (this.onComplete) {
            this.onComplete();
        }
    }

    reset() {
        this.stop();
        this.displayedText = '';
        this.textObject.setText('');
        this.currentIndex = 0;
        this.isComplete = false;
    }

    getText() {
        return this.text;
    }

    getDisplayedText() {
        return this.displayedText;
    }

    append(text) {
        this.text += text;
        this.displayedText += text;
        this.textObject.setText(this.displayedText);
    }

    isTyping() {
        return this.isPlaying && !this.isPaused;
    }

    destroy() {
        this.stop();
        if (this.textObject) {
            this.textObject.destroy();
        }
    }
}