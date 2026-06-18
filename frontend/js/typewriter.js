/**
 * 打字机效果模块
 * 实现逐字显示、光标闪烁、暂停/继续功能
 */
const Typewriter = (() => {
    /**
     * 创建一个打字机实例
     * @param {HTMLElement} textElement - 显示文字的元素
     * @param {HTMLElement} cursorElement - 光标元素
     * @param {Object} options - 配置选项
     * @param {number} options.speed - 打字速度 (ms/字)，默认 80
     * @param {number} options.startDelay - 开始前延迟 (ms)，默认 300
     * @param {boolean} options.loop - 是否循环，默认 false
     * @param {Function} options.onComplete - 完成回调
     * @param {Function} options.onChar - 每字回调
     */
    function create(textElement, cursorElement, options = {}) {
        const config = {
            speed: options.speed || 80,
            startDelay: options.startDelay ?? 300,
            loop: options.loop || false,
            onComplete: options.onComplete || null,
            onChar: options.onChar || null,
        };

        let currentText = '';
        let targetText = '';
        let charIndex = 0;
        let timer = null;
        let isPaused = false;
        let isRunning = false;
        let isComplete = false;

        /**
         * 开始打字
         * @param {string} text - 要显示的文字
         * @param {boolean} append - 是否追加（而非替换），默认 false
         */
        function start(text, append = false) {
            // 清除之前的定时器
            stop();

            if (append && currentText) {
                targetText = currentText + text;
                charIndex = currentText.length;
            } else {
                targetText = text;
                charIndex = 0;
                currentText = '';
                if (textElement) textElement.textContent = '';
            }

            isRunning = true;
            isComplete = false;
            isPaused = false;

            if (cursorElement) {
                cursorElement.classList.remove('hidden');
            }

            // 延迟后开始
            timer = setTimeout(() => {
                typeNext();
            }, config.startDelay);
        }

        /**
         * 逐字输出
         */
        function typeNext() {
            if (!isRunning || isPaused) return;

            if (charIndex < targetText.length) {
                // 处理 HTML 标签（跳过标签不计入打字）
                if (targetText[charIndex] === '<') {
                    const closeIdx = targetText.indexOf('>', charIndex);
                    if (closeIdx !== -1) {
                        currentText = targetText.substring(0, closeIdx + 1);
                        charIndex = closeIdx + 1;
                        if (textElement) textElement.innerHTML = currentText;
                    }
                }

                currentText = targetText.substring(0, charIndex + 1);
                if (textElement) {
                    textElement.textContent = currentText;
                }

                charIndex++;

                if (config.onChar) {
                    config.onChar(currentText, charIndex, targetText.length);
                }

                // 标点符号后稍作停顿
                const char = targetText[charIndex - 1];
                let delay = config.speed;
                if (char && "，。！？；：、…—\"\"''（）《》".includes(char)) {
                    delay = config.speed * 2.5;
                } else if (char && '，！？；：'.includes(char)) {
                    delay = config.speed * 2;
                }

                timer = setTimeout(typeNext, delay);
            } else {
                // 完成
                isComplete = true;
                isRunning = false;

                if (config.onComplete) {
                    config.onComplete(currentText);
                }

                if (config.loop) {
                    // 循环：等待后重新开始
                    timer = setTimeout(() => {
                        start(targetText);
                    }, 2000);
                }
            }
        }

        /**
         * 暂停
         */
        function pause() {
            if (!isRunning || isPaused) return;
            isPaused = true;
            if (timer) {
                clearTimeout(timer);
                timer = null;
            }
        }

        /**
         * 继续
         */
        function resume() {
            if (!isPaused) return;
            isPaused = false;
            typeNext();
        }

        /**
         * 切换暂停/继续
         */
        function toggle() {
            if (isPaused) {
                resume();
            } else {
                pause();
            }
        }

        /**
         * 跳过，直接显示全部文字
         */
        function skip() {
            if (!isRunning && !isPaused) return;
            if (timer) {
                clearTimeout(timer);
                timer = null;
            }
            currentText = targetText;
            charIndex = targetText.length;
            if (textElement) {
                textElement.textContent = currentText;
            }
            isComplete = true;
            isRunning = false;
            isPaused = false;

            if (config.onComplete) {
                config.onComplete(currentText);
            }
        }

        /**
         * 停止
         */
        function stop() {
            if (timer) {
                clearTimeout(timer);
                timer = null;
            }
            isRunning = false;
            isPaused = false;
        }

        /**
         * 重置
         */
        function reset() {
            stop();
            currentText = '';
            targetText = '';
            charIndex = 0;
            isComplete = false;
            if (textElement) textElement.textContent = '';
            if (cursorElement) cursorElement.classList.add('hidden');
        }

        /**
         * 获取当前状态
         */
        function getState() {
            return {
                isRunning,
                isPaused,
                isComplete,
                currentText,
                progress: targetText ? charIndex / targetText.length : 0,
            };
        }

        /**
         * 设置速度
         */
        function setSpeed(speed) {
            config.speed = speed;
        }

        // 公共 API
        return {
            start,
            pause,
            resume,
            toggle,
            skip,
            stop,
            reset,
            getState,
            setSpeed,
            get isRunning() { return isRunning; },
            get isComplete() { return isComplete; },
            get isPaused() { return isPaused; },
        };
    }

    return { create };
})();