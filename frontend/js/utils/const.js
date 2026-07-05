// 游戏常量
const GAME_CONFIG = {
    width: 1200,
    height: 800,
    backgroundColor: '#87CEEB',
    tileSize: 64,
};

// NPC 配置
const NPC_CONFIG = {
    guide: {
        name: '小荷',
        position: { x: 600, y: 400 },
        color: 0xFF69B4,
        scale: 1.5,
    },
};

// WebSocket 配置（支持本地开发和 Render 部署）
const getWebSocketUrl = () => {
    const backendUrl = window.BACKEND_URL || window.location.hostname;
    const isProduction = window.location.hostname.includes('.onrender.com');

    if (isProduction) {
        const wsHost = backendUrl.includes('://')
            ? backendUrl.replace(/^https?:\/\//, '').replace(/\/$/, '')
            : backendUrl;
        return `wss://${wsHost}/ws/game`;
    }

    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${wsProtocol}//${window.location.hostname}:8080/ws/game`;
};

const WS_CONFIG = {
    url: getWebSocketUrl(),
    reconnectInterval: 3000,
    pingInterval: 30000,
};


// 消息类型
const MESSAGE_TYPES = {
    CONNECTION: 'CONNECTION',
    CHAT_MESSAGE: 'CHAT_MESSAGE',
    PING: 'PING',
    WELCOME: 'WELCOME',
    NPC_REPLY: 'NPC_REPLY',
    ERROR: 'ERROR',
    PONG: 'PONG',
    STREAM_EVENT: 'STREAM_EVENT',
};

// 表情符号
const EMOJIS = {
    happy: '😊',
    sad: '😢',
    angry: '😠',
    confused: '😕',
    excited: '🎉',
    neutral: '😊',
};

// 颜色配置
const COLORS = {
    dialogBox: 0xFFFFFF,
    dialogBorder: 0x4A4A4A,
    playerName: 0x333333,
    npcName: 0xFF69B4,
    dialogText: 0x333333,
    inputBox: 0xFFFFFF,
    inputBorder: 0xCCCCCC,
    inputText: 0x333333,
    inputPlaceholder: 0x999999,
};

// ========== Canvas Texture 工具函数 ==========

// 纹理 ID 计数器（确保每个纹理 key 唯一）
let _textureIdCounter = 0;

// 十六进制颜色转 CSS rgb 字符串
function hexToCSS(hex) {
    const r = (hex >> 16) & 0xFF;
    const g = (hex >> 8) & 0xFF;
    const b = hex & 0xFF;
    return `rgb(${r},${g},${b})`;
}

// 创建椭圆纹理
function createEllipseTexture(scene, width, height, fillColor, fillAlpha, strokeWidth, strokeColor) {
    const key = '_texEllipse_' + (++_textureIdCounter);
    const pad = strokeWidth ? Math.ceil(strokeWidth / 2) + 1 : 1;
    const cw = Math.ceil(width + pad * 2);
    const ch = Math.ceil(height + pad * 2);

    const texture = scene.textures.createCanvas(key, cw, ch);
    const canvas = texture.getSourceImage();
    const ctx = canvas.getContext('2d');

    ctx.clearRect(0, 0, cw, ch);

    if (fillColor !== undefined) {
        ctx.fillStyle = hexToCSS(fillColor);
        ctx.globalAlpha = fillAlpha !== undefined ? fillAlpha : 1;
        ctx.beginPath();
        ctx.ellipse(cw / 2, ch / 2, width / 2, height / 2, 0, 0, Math.PI * 2);
        ctx.fill();
    }

    if (strokeWidth && strokeColor !== undefined) {
        ctx.globalAlpha = 1;
        ctx.strokeStyle = hexToCSS(strokeColor);
        ctx.lineWidth = strokeWidth;
        ctx.beginPath();
        ctx.ellipse(cw / 2, ch / 2, width / 2, height / 2, 0, 0, Math.PI * 2);
        ctx.stroke();
    }

    // 确保 renderer 已设置（scene.create() 期间可能还未设置）
    if (scene.sys.game && scene.sys.game.renderer) {
        texture.renderer = scene.sys.game.renderer;
    }

    // 刷新纹理
    if (texture.refresh) {
        texture.refresh();
    } else {
        texture.update();
    }

    return key;
}

// 创建圆形纹理
function createCircleTexture(scene, radius, fillColor, fillAlpha, strokeWidth, strokeColor) {
    return createEllipseTexture(scene, radius * 2, radius * 2, fillColor, fillAlpha, strokeWidth, strokeColor);
}

// 创建多边形纹理
function createPolygonTexture(scene, points, fillColor, fillAlpha, strokeWidth, strokeColor) {
    const key = '_texPoly_' + (++_textureIdCounter);

    // 计算包围盒
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    for (let i = 0; i < points.length; i += 2) {
        minX = Math.min(minX, points[i]);
        minY = Math.min(minY, points[i + 1]);
        maxX = Math.max(maxX, points[i]);
        maxY = Math.max(maxY, points[i + 1]);
    }

    // 计算中心点（用于居中绘制）
    const centerX = (minX + maxX) / 2;
    const centerY = (minY + maxY) / 2;

    const pad = strokeWidth ? Math.ceil(strokeWidth / 2) + 1 : 1;
    const shapeW = maxX - minX;
    const shapeH = maxY - minY;
    const cw = Math.ceil(shapeW + pad * 2);
    const ch = Math.ceil(shapeH + pad * 2);

    // 偏移量使多边形居中于 canvas
    const ox = cw / 2 - centerX;
    const oy = ch / 2 - centerY;

    const texture = scene.textures.createCanvas(key, cw, ch);
    const canvas = texture.getSourceImage();
    const ctx = canvas.getContext('2d');

    ctx.clearRect(0, 0, cw, ch);

    if (fillColor !== undefined) {
        ctx.fillStyle = hexToCSS(fillColor);
        ctx.globalAlpha = fillAlpha !== undefined ? fillAlpha : 1;
        ctx.beginPath();
        ctx.moveTo(points[0] + ox, points[1] + oy);
        for (let i = 2; i < points.length; i += 2) {
            ctx.lineTo(points[i] + ox, points[i + 1] + oy);
        }
        ctx.closePath();
        ctx.fill();
    }

    if (strokeWidth && strokeColor !== undefined) {
        ctx.globalAlpha = 1;
        ctx.strokeStyle = hexToCSS(strokeColor);
        ctx.lineWidth = strokeWidth;
        ctx.beginPath();
        ctx.moveTo(points[0] + ox, points[1] + oy);
        for (let i = 2; i < points.length; i += 2) {
            ctx.lineTo(points[i] + ox, points[i + 1] + oy);
        }
        ctx.closePath();
        ctx.stroke();
    }

    // 确保 renderer 已设置（scene.create() 期间可能还未设置）
    if (scene.sys.game && scene.sys.game.renderer) {
        texture.renderer = scene.sys.game.renderer;
    }

    // 刷新纹理
    if (texture.refresh) {
        texture.refresh();
    } else {
        texture.update();
    }

    return key;
}
