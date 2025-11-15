# Java 客户端集成指南 ☕

> 本文档提供基于 go-wsc 服务端的 Java WebSocket 客户端完整实现方案，包含企业级特性和最佳实践。

## 📖 目录

- [Maven 依赖配置](#-maven-依赖配置)
- [基础 Java 客户端](#-基础-java-客户端)
- [高级功能实现](#-高级功能实现)
- [Spring Boot 集成](#-spring-boot-集成)
- [ACK 消息确认](#-ack-消息确认)
- [连接池管理](#-连接池管理)
- [最佳实践](#-最佳实践)

## 📦 Maven 依赖配置

### 基础依赖

```xml
<dependencies>
    <!-- Java WebSocket API -->
    <dependency>
        <groupId>javax.websocket</groupId>
        <artifactId>javax.websocket-api</artifactId>
        <version>1.1</version>
    </dependency>
    
    <!-- Tyrus WebSocket 实现 -->
    <dependency>
        <groupId>org.glassfish.tyrus.bundles</groupId>
        <artifactId>tyrus-standalone-client</artifactId>
        <version>2.1.4</version>
    </dependency>
    
    <!-- JSON 处理 -->
    <dependency>
        <groupId>com.fasterxml.jackson.core</groupId>
        <artifactId>jackson-databind</artifactId>
        <version>2.15.2</version>
    </dependency>
    
    <!-- 日志框架 -->
    <dependency>
        <groupId>org.slf4j</groupId>
        <artifactId>slf4j-api</artifactId>
        <version>2.0.7</version>
    </dependency>
    
    <!-- Logback 实现 -->
    <dependency>
        <groupId>ch.qos.logback</groupId>
        <artifactId>logback-classic</artifactId>
        <version>1.4.8</version>
    </dependency>
</dependencies>
```

### Gradle 配置

```gradle
dependencies {
    implementation 'javax.websocket:javax.websocket-api:1.1'
    implementation 'org.glassfish.tyrus.bundles:tyrus-standalone-client:2.1.4'
    implementation 'com.fasterxml.jackson.core:jackson-databind:2.15.2'
    implementation 'org.slf4j:slf4j-api:2.0.7'
    implementation 'ch.qos.logback:logback-classic:1.4.8'
}
```

## 🚀 基础 Java 客户端

### 核心客户端类

```java
package com.example.wsc;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import javax.websocket.*;
import java.net.URI;
import java.nio.ByteBuffer;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Consumer;

/**
 * 高级 WebSocket 客户端 - 基于 go-wsc 设计理念的 Java 实现
 * 
 * 特性：
 * - 自动重连机制
 * - 心跳检测
 * - 消息队列缓冲
 * - 事件驱动架构
 * - 线程安全
 */
@ClientEndpoint
public class AdvancedWebSocketClient {
    
    private static final Logger logger = LoggerFactory.getLogger(AdvancedWebSocketClient.class);
    
    // 配置参数
    private final WSConfig config;
    private final String serverUrl;
    private final ObjectMapper objectMapper;
    
    // 连接管理
    private Session session;
    private final AtomicBoolean connected = new AtomicBoolean(false);
    private final AtomicBoolean connecting = new AtomicBoolean(false);
    private final AtomicInteger reconnectAttempts = new AtomicInteger(0);
    
    // 线程池和调度器
    private final ScheduledExecutorService scheduler;
    private final ExecutorService messageExecutor;
    private ScheduledFuture<?> heartbeatTask;
    private ScheduledFuture<?> reconnectTask;
    
    // 消息队列
    private final BlockingQueue<WSMessage> messageQueue;
    private final AtomicInteger messageIdGenerator = new AtomicInteger(0);
    
    // 事件处理器
    private final ConcurrentHashMap<String, CopyOnWriteArrayList<Consumer<?>>> eventHandlers = new ConcurrentHashMap<>();
    
    /**
     * WebSocket 配置类
     */
    public static class WSConfig {
        private boolean autoReconnect = true;
        private int maxReconnectAttempts = 10;
        private long reconnectIntervalMs = 2000;
        private long maxReconnectIntervalMs = 30000;
        private double reconnectBackoffFactor = 1.5;
        private long heartbeatIntervalMs = 30000;
        private int messageBufferSize = 256;
        private int maxMessageSizeBytes = 1024 * 1024; // 1MB
        private long connectionTimeoutMs = 10000;
        
        // Getter 和 Setter 方法...
        public WSConfig setAutoReconnect(boolean autoReconnect) {
            this.autoReconnect = autoReconnect;
            return this;
        }
        
        public WSConfig setMaxReconnectAttempts(int maxReconnectAttempts) {
            this.maxReconnectAttempts = maxReconnectAttempts;
            return this;
        }
        
        public WSConfig setReconnectIntervalMs(long reconnectIntervalMs) {
            this.reconnectIntervalMs = reconnectIntervalMs;
            return this;
        }
        
        public WSConfig setHeartbeatIntervalMs(long heartbeatIntervalMs) {
            this.heartbeatIntervalMs = heartbeatIntervalMs;
            return this;
        }
        
        public WSConfig setMessageBufferSize(int messageBufferSize) {
            this.messageBufferSize = messageBufferSize;
            return this;
        }
        
        // 其他 getter 方法
        public boolean isAutoReconnect() { return autoReconnect; }
        public int getMaxReconnectAttempts() { return maxReconnectAttempts; }
        public long getReconnectIntervalMs() { return reconnectIntervalMs; }
        public long getMaxReconnectIntervalMs() { return maxReconnectIntervalMs; }
        public double getReconnectBackoffFactor() { return reconnectBackoffFactor; }
        public long getHeartbeatIntervalMs() { return heartbeatIntervalMs; }
        public int getMessageBufferSize() { return messageBufferSize; }
        public int getMaxMessageSizeBytes() { return maxMessageSizeBytes; }
        public long getConnectionTimeoutMs() { return connectionTimeoutMs; }
    }
    
    /**
     * WebSocket 消息类
     */
    public static class WSMessage {
        private String id;
        private String type;
        private Object data;
        private long timestamp;
        
        public WSMessage() {}
        
        public WSMessage(String type, Object data) {
            this.type = type;
            this.data = data;
            this.timestamp = System.currentTimeMillis();
        }
        
        // Getter 和 Setter 方法
        public String getId() { return id; }
        public void setId(String id) { this.id = id; }
        public String getType() { return type; }
        public void setType(String type) { this.type = type; }
        public Object getData() { return data; }
        public void setData(Object data) { this.data = data; }
        public long getTimestamp() { return timestamp; }
        public void setTimestamp(long timestamp) { this.timestamp = timestamp; }
    }
    
    /**
     * 构造函数
     */
    public AdvancedWebSocketClient(String serverUrl, WSConfig config) {
        this.serverUrl = serverUrl;
        this.config = config != null ? config : new WSConfig();
        this.objectMapper = new ObjectMapper();
        this.scheduler = Executors.newScheduledThreadPool(2);
        this.messageExecutor = Executors.newCachedThreadPool();
        this.messageQueue = new LinkedBlockingQueue<>(this.config.getMessageBufferSize());
        
        initializeEventHandlers();
    }
    
    /**
     * 默认构造函数
     */
    public AdvancedWebSocketClient(String serverUrl) {
        this(serverUrl, new WSConfig());
    }
    
    /**
     * 初始化事件处理器
     */
    private void initializeEventHandlers() {
        String[] events = {
            "connected", "disconnected", "connectError", "message",
            "binaryMessage", "messageSent", "sendError", "close",
            "ping", "pong", "reconnecting", "messageQueued"
        };
        
        for (String event : events) {
            eventHandlers.put(event, new CopyOnWriteArrayList<>());
        }
    }
    
    /**
     * 建立连接
     */
    public CompletableFuture<Void> connect() {
        return CompletableFuture.runAsync(() -> {
            if (connecting.get() || connected.get()) {
                return;
            }
            
            connecting.set(true);
            emitEvent("reconnecting", reconnectAttempts.get());
            
            try {
                WebSocketContainer container = ContainerProvider.getWebSocketContainer();
                container.setDefaultMaxSessionIdleTimeout(0);
                
                URI uri = new URI(serverUrl);
                logger.info("🔄 正在连接到 WebSocket 服务器: {}", serverUrl);
                
                // 设置连接超时
                CompletableFuture<Session> connectionFuture = CompletableFuture.supplyAsync(() -> {
                    try {
                        return container.connectToServer(this, uri);
                    } catch (Exception e) {
                        throw new RuntimeException(e);
                    }
                });
                
                this.session = connectionFuture.get(config.getConnectionTimeoutMs(), TimeUnit.MILLISECONDS);
                
            } catch (Exception e) {
                connecting.set(false);
                logger.error("❌ WebSocket 连接失败: {}", e.getMessage(), e);
                emitEvent("connectError", e);
                
                // 自动重连
                if (config.isAutoReconnect() && reconnectAttempts.get() < config.getMaxReconnectAttempts()) {
                    scheduleReconnect();
                }
                throw new RuntimeException("连接失败", e);
            }
        }, messageExecutor);
    }
    
    /**
     * 连接打开时的回调
     */
    @OnOpen
    public void onOpen(Session session) {
        this.session = session;
        connecting.set(false);
        connected.set(true);
        reconnectAttempts.set(0);
        
        logger.info("✅ WebSocket 连接已建立: {}", session.getId());
        emitEvent("connected");
        
        // 开始心跳检测
        startHeartbeat();
        
        // 发送队列中的消息
        flushMessageQueue();
    }
    
    /**
     * 接收文本消息
     */
    @OnMessage
    public void onMessage(String message) {
        logger.debug("📨 收到文本消息: {}", message);
        
        try {
            // 处理心跳响应
            if ("pong".equals(message)) {
                emitEvent("pong", message);
                return;
            }
            
            // 尝试解析为 JSON 消息
            try {
                WSMessage wsMessage = objectMapper.readValue(message, WSMessage.class);
                emitEvent("message", wsMessage);
            } catch (Exception e) {
                // 普通文本消息
                WSMessage wsMessage = new WSMessage("text", message);
                emitEvent("message", wsMessage);
            }
            
        } catch (Exception e) {
            logger.error("❌ 处理消息时出错: {}", e.getMessage(), e);
        }
    }
    
    /**
     * 接收二进制消息
     */
    @OnMessage
    public void onMessage(ByteBuffer buffer) {
        logger.debug("📦 收到二进制消息: {} 字节", buffer.remaining());
        
        byte[] data = new byte[buffer.remaining()];
        buffer.get(data);
        emitEvent("binaryMessage", data);
    }
    
    /**
     * 连接关闭时的回调
     */
    @OnClose
    public void onClose(Session session, CloseReason closeReason) {
        connected.set(false);
        connecting.set(false);
        stopHeartbeat();
        
        logger.info("🔒 WebSocket 连接关闭: code={}, reason={}", 
                   closeReason.getCloseCode().getCode(), closeReason.getReasonPhrase());
        
        emitEvent("close", closeReason.getCloseCode().getCode(), closeReason.getReasonPhrase());
        emitEvent("disconnected", new RuntimeException("连接关闭: " + closeReason.getReasonPhrase()));
        
        // 自动重连
        if (config.isAutoReconnect() && reconnectAttempts.get() < config.getMaxReconnectAttempts()) {
            scheduleReconnect();
        }
    }
    
    /**
     * 连接错误时的回调
     */
    @OnError
    public void onError(Session session, Throwable throwable) {
        logger.error("❌ WebSocket 连接错误: {}", throwable.getMessage(), throwable);
        emitEvent("connectError", throwable);
    }
    
    /**
     * 发送文本消息
     */
    public CompletableFuture<Void> sendText(String message) {
        return CompletableFuture.runAsync(() -> {
            if (!isConnected()) {
                if (config.isAutoReconnect() && messageQueue.remainingCapacity() > 0) {
                    WSMessage wsMessage = new WSMessage("text", message);
                    wsMessage.setId(String.valueOf(messageIdGenerator.incrementAndGet()));
                    messageQueue.offer(wsMessage);
                    emitEvent("messageQueued", wsMessage);
                } else {
                    throw new RuntimeException("WebSocket 未连接且消息队列已满");
                }
                return;
            }
            
            try {
                session.getBasicRemote().sendText(message);
                emitEvent("messageSent", message);
                logger.debug("📤 发送文本消息: {}", message);
            } catch (Exception e) {
                logger.error("❌ 发送文本消息失败: {}", e.getMessage(), e);
                emitEvent("sendError", e);
                throw new RuntimeException("发送失败", e);
            }
        }, messageExecutor);
    }
    
    /**
     * 发送二进制消息
     */
    public CompletableFuture<Void> sendBinary(byte[] data) {
        return CompletableFuture.runAsync(() -> {
            if (!isConnected()) {
                if (config.isAutoReconnect() && messageQueue.remainingCapacity() > 0) {
                    WSMessage wsMessage = new WSMessage("binary", data);
                    wsMessage.setId(String.valueOf(messageIdGenerator.incrementAndGet()));
                    messageQueue.offer(wsMessage);
                    emitEvent("messageQueued", wsMessage);
                } else {
                    throw new RuntimeException("WebSocket 未连接且消息队列已满");
                }
                return;
            }
            
            try {
                ByteBuffer buffer = ByteBuffer.wrap(data);
                session.getBasicRemote().sendBinary(buffer);
                emitEvent("messageSent", data);
                logger.debug("📤 发送二进制消息: {} 字节", data.length);
            } catch (Exception e) {
                logger.error("❌ 发送二进制消息失败: {}", e.getMessage(), e);
                emitEvent("sendError", e);
                throw new RuntimeException("发送失败", e);
            }
        }, messageExecutor);
    }
    
    /**
     * 发送 JSON 消息
     */
    public CompletableFuture<Void> sendJSON(Object obj) {
        return CompletableFuture.runAsync(() -> {
            try {
                String json = objectMapper.writeValueAsString(obj);
                sendText(json).join();
            } catch (Exception e) {
                logger.error("❌ JSON 序列化失败: {}", e.getMessage(), e);
                throw new RuntimeException("JSON 序列化失败", e);
            }
        }, messageExecutor);
    }
    
    /**
     * 发送 WebSocket 消息对象
     */
    public CompletableFuture<Void> sendMessage(WSMessage message) {
        message.setId(String.valueOf(messageIdGenerator.incrementAndGet()));
        message.setTimestamp(System.currentTimeMillis());
        return sendJSON(message);
    }
    
    /**
     * 检查连接状态
     */
    public boolean isConnected() {
        return connected.get() && session != null && session.isOpen();
    }
    
    /**
     * 断开连接
     */
    public void disconnect() {
        config.autoReconnect = false; // 停止自动重连
        
        stopHeartbeat();
        cancelReconnectTask();
        
        if (session != null) {
            try {
                session.close(new CloseReason(CloseReason.CloseCodes.NORMAL_CLOSURE, "客户端主动断开"));
            } catch (Exception e) {
                logger.error("❌ 关闭连接时出错: {}", e.getMessage(), e);
            }
        }
        
        connected.set(false);
        connecting.set(false);
    }
    
    /**
     * 关闭客户端并释放资源
     */
    public void shutdown() {
        disconnect();
        
        if (!scheduler.isShutdown()) {
            scheduler.shutdown();
            try {
                if (!scheduler.awaitTermination(5, TimeUnit.SECONDS)) {
                    scheduler.shutdownNow();
                }
            } catch (InterruptedException e) {
                scheduler.shutdownNow();
                Thread.currentThread().interrupt();
            }
        }
        
        if (!messageExecutor.isShutdown()) {
            messageExecutor.shutdown();
            try {
                if (!messageExecutor.awaitTermination(5, TimeUnit.SECONDS)) {
                    messageExecutor.shutdownNow();
                }
            } catch (InterruptedException e) {
                messageExecutor.shutdownNow();
                Thread.currentThread().interrupt();
            }
        }
        
        logger.info("🛑 WebSocket 客户端已关闭");
    }
    
    /**
     * 事件监听器注册
     */
    public <T> AdvancedWebSocketClient on(String event, Consumer<T> handler) {
        eventHandlers.get(event).add(handler);
        return this;
    }
    
    /**
     * 移除事件监听器
     */
    public <T> AdvancedWebSocketClient off(String event, Consumer<T> handler) {
        if (handler != null) {
            eventHandlers.get(event).remove(handler);
        } else {
            eventHandlers.get(event).clear();
        }
        return this;
    }
    
    /**
     * 触发事件
     */
    @SuppressWarnings("unchecked")
    private void emitEvent(String event, Object... args) {
        CopyOnWriteArrayList<Consumer<?>> handlers = eventHandlers.get(event);
        if (handlers != null) {
            for (Consumer<?> handler : handlers) {
                try {
                    if (args.length == 0) {
                        ((Consumer<Void>) handler).accept(null);
                    } else if (args.length == 1) {
                        ((Consumer<Object>) handler).accept(args[0]);
                    } else {
                        ((Consumer<Object[]>) handler).accept(args);
                    }
                } catch (Exception e) {
                    logger.error("❌ 事件处理器执行错误 ({}): {}", event, e.getMessage(), e);
                }
            }
        }
    }
    
    /**
     * 开始心跳检测
     */
    private void startHeartbeat() {
        stopHeartbeat();
        
        if (config.getHeartbeatIntervalMs() > 0) {
            heartbeatTask = scheduler.scheduleAtFixedRate(() -> {
                if (isConnected()) {
                    try {
                        sendText("ping").join();
                    } catch (Exception e) {
                        logger.warn("⚠️ 发送心跳失败: {}", e.getMessage());
                    }
                }
            }, config.getHeartbeatIntervalMs(), config.getHeartbeatIntervalMs(), TimeUnit.MILLISECONDS);
            
            logger.debug("💓 心跳检测已启动，间隔: {}ms", config.getHeartbeatIntervalMs());
        }
    }
    
    /**
     * 停止心跳检测
     */
    private void stopHeartbeat() {
        if (heartbeatTask != null && !heartbeatTask.isCancelled()) {
            heartbeatTask.cancel(false);
            heartbeatTask = null;
            logger.debug("💔 心跳检测已停止");
        }
    }
    
    /**
     * 调度重连
     */
    private void scheduleReconnect() {
        cancelReconnectTask();
        
        long delay = Math.min(
            (long)(config.getReconnectIntervalMs() * Math.pow(config.getReconnectBackoffFactor(), reconnectAttempts.get())),
            config.getMaxReconnectIntervalMs()
        );
        
        logger.info("🔄 将在 {}ms 后尝试重连 ({}/{})", 
                   delay, reconnectAttempts.get() + 1, config.getMaxReconnectAttempts());
        
        reconnectTask = scheduler.schedule(() -> {
            reconnectAttempts.incrementAndGet();
            try {
                connect().join();
            } catch (Exception e) {
                logger.error("❌ 重连失败: {}", e.getMessage());
            }
        }, delay, TimeUnit.MILLISECONDS);
    }
    
    /**
     * 取消重连任务
     */
    private void cancelReconnectTask() {
        if (reconnectTask != null && !reconnectTask.isCancelled()) {
            reconnectTask.cancel(false);
            reconnectTask = null;
        }
    }
    
    /**
     * 发送队列中的消息
     */
    private void flushMessageQueue() {
        WSMessage message;
        while ((message = messageQueue.poll()) != null && isConnected()) {
            try {
                if ("text".equals(message.getType())) {
                    sendText((String) message.getData()).join();
                } else if ("binary".equals(message.getType())) {
                    sendBinary((byte[]) message.getData()).join();
                } else {
                    sendMessage(message).join();
                }
                logger.debug("📤 队列消息已发送: {}", message.getId());
            } catch (Exception e) {
                logger.error("❌ 发送队列消息失败: {}", e.getMessage(), e);
                break;
            }
        }
    }
    
    /**
     * 获取连接统计信息
     */
    public ConnectionStats getConnectionStats() {
        return new ConnectionStats(
            connected.get(),
            connecting.get(),
            reconnectAttempts.get(),
            messageQueue.size(),
            session != null ? session.getId() : null
        );
    }
    
    /**
     * 连接统计信息类
     */
    public static class ConnectionStats {
        private final boolean connected;
        private final boolean connecting;
        private final int reconnectAttempts;
        private final int queuedMessages;
        private final String sessionId;
        
        public ConnectionStats(boolean connected, boolean connecting, int reconnectAttempts, 
                             int queuedMessages, String sessionId) {
            this.connected = connected;
            this.connecting = connecting;
            this.reconnectAttempts = reconnectAttempts;
            this.queuedMessages = queuedMessages;
            this.sessionId = sessionId;
        }
        
        // Getter 方法
        public boolean isConnected() { return connected; }
        public boolean isConnecting() { return connecting; }
        public int getReconnectAttempts() { return reconnectAttempts; }
        public int getQueuedMessages() { return queuedMessages; }
        public String getSessionId() { return sessionId; }
        
        @Override
        public String toString() {
            return String.format("ConnectionStats{connected=%s, connecting=%s, reconnectAttempts=%d, queuedMessages=%d, sessionId='%s'}", 
                                connected, connecting, reconnectAttempts, queuedMessages, sessionId);
        }
    }
}
```

## 🎯 基础使用示例

### 简单连接示例

```java
package com.example.demo;

import com.example.wsc.AdvancedWebSocketClient;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

public class BasicWebSocketDemo {
    private static final Logger logger = LoggerFactory.getLogger(BasicWebSocketDemo.class);
    
    public static void main(String[] args) {
        // 1. 创建客户端
        AdvancedWebSocketClient client = new AdvancedWebSocketClient("ws://localhost:8080/ws");
        
        // 2. 设置事件监听器
        client
            .on("connected", (Void unused) -> {
                logger.info("✅ 连接成功");
                // 发送认证消息
                try {
                    client.sendJSON(new AuthMessage("your-token")).join();
                } catch (Exception e) {
                    logger.error("发送认证消息失败", e);
                }
            })
            .on("message", (AdvancedWebSocketClient.WSMessage message) -> {
                logger.info("📨 收到消息: type={}, data={}", message.getType(), message.getData());
                
                // 处理不同类型的消息
                handleMessage(message);
            })
            .on("disconnected", (Throwable error) -> {
                logger.warn("⚠️ 连接断开: {}", error.getMessage());
            })
            .on("connectError", (Throwable error) -> {
                logger.error("❌ 连接错误: {}", error.getMessage());
            });
        
        // 3. 建立连接
        try {
            client.connect().join();
            logger.info("🚀 WebSocket 客户端启动成功");
            
            // 4. 模拟发送消息
            simulateMessageSending(client);
            
            // 5. 保持程序运行
            Thread.sleep(60000); // 运行 1 分钟
            
        } catch (Exception e) {
            logger.error("❌ 客户端运行失败", e);
        } finally {
            // 6. 关闭客户端
            client.shutdown();
        }
    }
    
    private static void handleMessage(AdvancedWebSocketClient.WSMessage message) {
        switch (message.getType()) {
            case "chat":
                logger.info("💬 聊天消息: {}", message.getData());
                break;
            case "notification":
                logger.info("🔔 通知消息: {}", message.getData());
                break;
            case "system":
                logger.info("⚙️ 系统消息: {}", message.getData());
                break;
            default:
                logger.info("📦 未知消息类型: {}", message);
        }
    }
    
    private static void simulateMessageSending(AdvancedWebSocketClient client) {
        // 使用定时器定期发送消息
        java.util.Timer timer = new java.util.Timer();
        timer.scheduleAtFixedRate(new java.util.TimerTask() {
            @Override
            public void run() {
                if (client.isConnected()) {
                    try {
                        AdvancedWebSocketClient.WSMessage heartbeat = 
                            new AdvancedWebSocketClient.WSMessage("heartbeat", 
                                java.util.Map.of("timestamp", System.currentTimeMillis()));
                        client.sendMessage(heartbeat);
                    } catch (Exception e) {
                        logger.error("发送心跳失败", e);
                    }
                }
            }
        }, 5000, 30000); // 5秒后开始，每30秒发送一次
    }
    
    // 认证消息类
    static class AuthMessage {
        private String type = "auth";
        private String token;
        
        public AuthMessage(String token) {
            this.token = token;
        }
        
        public String getType() { return type; }
        public String getToken() { return token; }
    }
}
```

## 🔧 高级功能实现

### ACK 消息确认机制

```java
package com.example.wsc;

import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

/**
 * ACK 消息确认管理器
 */
public class ACKManager {
    private final AdvancedWebSocketClient client;
    private final ConcurrentHashMap<String, CompletableFuture<AdvancedWebSocketClient.WSMessage>> pendingACKs = new ConcurrentHashMap<>();
    private final long defaultTimeoutMs = 30000; // 30秒超时
    
    public ACKManager(AdvancedWebSocketClient client) {
        this.client = client;
        setupACKHandler();
    }
    
    /**
     * 设置 ACK 处理器
     */
    private void setupACKHandler() {
        client.on("message", (AdvancedWebSocketClient.WSMessage message) -> {
            if ("ack".equals(message.getType())) {
                handleACKResponse(message);
            }
        });
    }
    
    /**
     * 发送需要确认的消息
     */
    public CompletableFuture<AdvancedWebSocketClient.WSMessage> sendACKMessage(AdvancedWebSocketClient.WSMessage message) {
        return sendACKMessage(message, defaultTimeoutMs);
    }
    
    /**
     * 发送需要确认的消息（带超时）
     */
    public CompletableFuture<AdvancedWebSocketClient.WSMessage> sendACKMessage(AdvancedWebSocketClient.WSMessage message, long timeoutMs) {
        String messageId = message.getId();
        if (messageId == null) {
            messageId = String.valueOf(System.currentTimeMillis());
            message.setId(messageId);
        }
        
        // 创建等待确认的 Future
        CompletableFuture<AdvancedWebSocketClient.WSMessage> ackFuture = new CompletableFuture<>();
        pendingACKs.put(messageId, ackFuture);
        
        // 发送消息
        return client.sendMessage(message)
            .thenCompose(v -> ackFuture)
            .orTimeout(timeoutMs, TimeUnit.MILLISECONDS)
            .whenComplete((result, throwable) -> {
                // 清理待确认消息
                pendingACKs.remove(messageId);
                
                if (throwable instanceof TimeoutException) {
                    client.emitEvent("ackTimeout", messageId);
                }
            });
    }
    
    /**
     * 处理 ACK 响应
     */
    private void handleACKResponse(AdvancedWebSocketClient.WSMessage ackMessage) {
        Object data = ackMessage.getData();
        if (data instanceof java.util.Map) {
            @SuppressWarnings("unchecked")
            java.util.Map<String, Object> ackData = (java.util.Map<String, Object>) data;
            String originalMessageId = (String) ackData.get("messageId");
            
            if (originalMessageId != null) {
                CompletableFuture<AdvancedWebSocketClient.WSMessage> pendingFuture = pendingACKs.remove(originalMessageId);
                if (pendingFuture != null) {
                    pendingFuture.complete(ackMessage);
                }
            }
        }
    }
    
    /**
     * 手动发送 ACK 确认
     */
    public CompletableFuture<Void> sendACK(String messageId, String status, String message) {
        AdvancedWebSocketClient.WSMessage ackMessage = new AdvancedWebSocketClient.WSMessage("ack", 
            java.util.Map.of(
                "messageId", messageId,
                "status", status,
                "message", message,
                "timestamp", System.currentTimeMillis()
            )
        );
        
        return client.sendMessage(ackMessage);
    }
    
    /**
     * 获取待确认消息数量
     */
    public int getPendingACKCount() {
        return pendingACKs.size();
    }
    
    /**
     * 获取待确认消息ID列表
     */
    public java.util.Set<String> getPendingACKIds() {
        return new java.util.HashSet<>(pendingACKs.keySet());
    }
}
```

### ACK 使用示例

```java
public class ACKDemo {
    public static void main(String[] args) {
        AdvancedWebSocketClient client = new AdvancedWebSocketClient("ws://localhost:8080/ws");
        ACKManager ackManager = new ACKManager(client);
        
        client
            .on("connected", (Void unused) -> {
                System.out.println("✅ 连接成功，开始发送 ACK 消息");
                
                // 发送需要确认的重要消息
                AdvancedWebSocketClient.WSMessage importantMessage = 
                    new AdvancedWebSocketClient.WSMessage("important", 
                        java.util.Map.of("content", "这是一条重要消息", "priority", "high"));
                
                ackManager.sendACKMessage(importantMessage)
                    .thenAccept(ack -> {
                        System.out.println("✅ 消息已确认: " + ack.getData());
                    })
                    .exceptionally(throwable -> {
                        if (throwable.getCause() instanceof TimeoutException) {
                            System.err.println("⏰ ACK 超时: " + importantMessage.getId());
                        } else {
                            System.err.println("❌ ACK 失败: " + throwable.getMessage());
                        }
                        return null;
                    });
            })
            .on("message", (AdvancedWebSocketClient.WSMessage message) -> {
                // 自动回复 ACK（如果需要）
                if (!"ack".equals(message.getType()) && message.getId() != null) {
                    ackManager.sendACK(message.getId(), "success", "消息已接收");
                }
            });
        
        try {
            client.connect().join();
            Thread.sleep(10000); // 等待 10 秒
        } catch (Exception e) {
            e.printStackTrace();
        } finally {
            client.shutdown();
        }
    }
}
```

## 🌱 Spring Boot 集成

### Spring Boot 配置类

```java
package com.example.config;

import com.example.wsc.AdvancedWebSocketClient;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Scope;

@Configuration
public class WebSocketConfig {
    
    @Value("${websocket.server.url:ws://localhost:8080/ws}")
    private String serverUrl;
    
    @Value("${websocket.auto-reconnect:true}")
    private boolean autoReconnect;
    
    @Value("${websocket.max-reconnect-attempts:10}")
    private int maxReconnectAttempts;
    
    @Value("${websocket.heartbeat-interval:30000}")
    private long heartbeatInterval;
    
    @Bean
    @Scope("prototype")
    public AdvancedWebSocketClient webSocketClient() {
        AdvancedWebSocketClient.WSConfig config = new AdvancedWebSocketClient.WSConfig()
            .setAutoReconnect(autoReconnect)
            .setMaxReconnectAttempts(maxReconnectAttempts)
            .setHeartbeatIntervalMs(heartbeatInterval);
        
        return new AdvancedWebSocketClient(serverUrl, config);
    }
}
```

### WebSocket 服务类

```java
package com.example.service;

import com.example.wsc.AdvancedWebSocketClient;
import com.example.wsc.ACKManager;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.context.ApplicationContext;
import org.springframework.stereotype.Service;

import javax.annotation.PostConstruct;
import javax.annotation.PreDestroy;
import java.util.concurrent.CompletableFuture;

@Service
public class WebSocketService {
    private static final Logger logger = LoggerFactory.getLogger(WebSocketService.class);
    
    @Autowired
    private ApplicationContext applicationContext;
    
    private AdvancedWebSocketClient client;
    private ACKManager ackManager;
    
    @PostConstruct
    public void initialize() {
        client = applicationContext.getBean(AdvancedWebSocketClient.class);
        ackManager = new ACKManager(client);
        
        setupEventHandlers();
        connectToServer();
    }
    
    @PreDestroy
    public void cleanup() {
        if (client != null) {
            client.shutdown();
        }
    }
    
    private void setupEventHandlers() {
        client
            .on("connected", (Void unused) -> {
                logger.info("✅ WebSocket 服务连接成功");
            })
            .on("disconnected", (Throwable error) -> {
                logger.warn("⚠️ WebSocket 服务断开: {}", error.getMessage());
            })
            .on("message", (AdvancedWebSocketClient.WSMessage message) -> {
                logger.info("📨 收到消息: {}", message);
                // 在这里可以发布 Spring Event 或调用其他服务
                processMessage(message);
            });
    }
    
    private void connectToServer() {
        client.connect()
            .thenRun(() -> logger.info("🚀 WebSocket 服务启动成功"))
            .exceptionally(throwable -> {
                logger.error("❌ WebSocket 服务启动失败", throwable);
                return null;
            });
    }
    
    /**
     * 发送消息
     */
    public CompletableFuture<Void> sendMessage(String type, Object data) {
        AdvancedWebSocketClient.WSMessage message = new AdvancedWebSocketClient.WSMessage(type, data);
        return client.sendMessage(message);
    }
    
    /**
     * 发送需要确认的消息
     */
    public CompletableFuture<AdvancedWebSocketClient.WSMessage> sendACKMessage(String type, Object data) {
        AdvancedWebSocketClient.WSMessage message = new AdvancedWebSocketClient.WSMessage(type, data);
        return ackManager.sendACKMessage(message);
    }
    
    /**
     * 检查连接状态
     */
    public boolean isConnected() {
        return client != null && client.isConnected();
    }
    
    /**
     * 获取连接统计
     */
    public AdvancedWebSocketClient.ConnectionStats getConnectionStats() {
        return client != null ? client.getConnectionStats() : null;
    }
    
    private void processMessage(AdvancedWebSocketClient.WSMessage message) {
        // 实现具体的消息处理逻辑
        // 可以根据消息类型调用不同的处理方法
        switch (message.getType()) {
            case "notification":
                handleNotification(message);
                break;
            case "command":
                handleCommand(message);
                break;
            default:
                logger.info("收到未知类型消息: {}", message.getType());
        }
    }
    
    private void handleNotification(AdvancedWebSocketClient.WSMessage message) {
        // 处理通知消息
        logger.info("处理通知: {}", message.getData());
    }
    
    private void handleCommand(AdvancedWebSocketClient.WSMessage message) {
        // 处理命令消息
        logger.info("执行命令: {}", message.getData());
    }
}
```

### REST 控制器

```java
package com.example.controller;

import com.example.service.WebSocketService;
import com.example.wsc.AdvancedWebSocketClient;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.*;

import java.util.Map;
import java.util.concurrent.CompletableFuture;

@RestController
@RequestMapping("/api/websocket")
public class WebSocketController {
    
    @Autowired
    private WebSocketService webSocketService;
    
    /**
     * 发送消息
     */
    @PostMapping("/send")
    public CompletableFuture<ResponseEntity<String>> sendMessage(@RequestBody Map<String, Object> payload) {
        String type = (String) payload.get("type");
        Object data = payload.get("data");
        
        return webSocketService.sendMessage(type, data)
            .thenApply(v -> ResponseEntity.ok("消息发送成功"))
            .exceptionally(throwable -> ResponseEntity.internalServerError().body("发送失败: " + throwable.getMessage()));
    }
    
    /**
     * 发送 ACK 消息
     */
    @PostMapping("/send-ack")
    public CompletableFuture<ResponseEntity<Object>> sendACKMessage(@RequestBody Map<String, Object> payload) {
        String type = (String) payload.get("type");
        Object data = payload.get("data");
        
        return webSocketService.sendACKMessage(type, data)
            .thenApply(ack -> ResponseEntity.ok(Map.of("status", "confirmed", "ack", ack)))
            .exceptionally(throwable -> ResponseEntity.internalServerError().body(Map.of("status", "failed", "error", throwable.getMessage())));
    }
    
    /**
     * 获取连接状态
     */
    @GetMapping("/status")
    public ResponseEntity<Map<String, Object>> getStatus() {
        boolean connected = webSocketService.isConnected();
        AdvancedWebSocketClient.ConnectionStats stats = webSocketService.getConnectionStats();
        
        return ResponseEntity.ok(Map.of(
            "connected", connected,
            "stats", stats != null ? Map.of(
                "sessionId", stats.getSessionId(),
                "reconnectAttempts", stats.getReconnectAttempts(),
                "queuedMessages", stats.getQueuedMessages()
            ) : null
        ));
    }
}
```

## 🔧 连接池管理

### WebSocket 连接池

```java
package com.example.wsc;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.concurrent.BlockingQueue;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * WebSocket 连接池
 * 支持多个并发连接，适用于高并发场景
 */
public class WebSocketConnectionPool {
    private static final Logger logger = LoggerFactory.getLogger(WebSocketConnectionPool.class);
    
    private final String serverUrl;
    private final AdvancedWebSocketClient.WSConfig config;
    private final int poolSize;
    private final BlockingQueue<AdvancedWebSocketClient> availableConnections;
    private final AtomicInteger totalConnections = new AtomicInteger(0);
    
    public WebSocketConnectionPool(String serverUrl, AdvancedWebSocketClient.WSConfig config, int poolSize) {
        this.serverUrl = serverUrl;
        this.config = config;
        this.poolSize = poolSize;
        this.availableConnections = new LinkedBlockingQueue<>(poolSize);
        
        initializePool();
    }
    
    /**
     * 初始化连接池
     */
    private void initializePool() {
        for (int i = 0; i < poolSize; i++) {
            AdvancedWebSocketClient client = createConnection();
            if (client != null) {
                availableConnections.offer(client);
            }
        }
        logger.info("🏊 WebSocket 连接池初始化完成，池大小: {}", availableConnections.size());
    }
    
    /**
     * 创建新连接
     */
    private AdvancedWebSocketClient createConnection() {
        try {
            AdvancedWebSocketClient client = new AdvancedWebSocketClient(serverUrl, config);
            client.connect().join();
            totalConnections.incrementAndGet();
            
            // 设置连接断开时的处理
            client.on("disconnected", (Throwable error) -> {
                logger.warn("⚠️ 池中连接断开: {}", error.getMessage());
                // 从池中移除断开的连接
                availableConnections.remove(client);
                
                // 创建新连接补充池
                AdvancedWebSocketClient newClient = createConnection();
                if (newClient != null) {
                    availableConnections.offer(newClient);
                }
            });
            
            return client;
        } catch (Exception e) {
            logger.error("❌ 创建 WebSocket 连接失败: {}", e.getMessage(), e);
            return null;
        }
    }
    
    /**
     * 从池中获取连接
     */
    public AdvancedWebSocketClient getConnection() throws InterruptedException {
        AdvancedWebSocketClient client = availableConnections.take();
        
        // 检查连接是否有效
        if (!client.isConnected()) {
            // 连接无效，创建新连接
            AdvancedWebSocketClient newClient = createConnection();
            if (newClient != null) {
                client.shutdown();
                return newClient;
            }
        }
        
        return client;
    }
    
    /**
     * 归还连接到池中
     */
    public void returnConnection(AdvancedWebSocketClient client) {
        if (client != null && client.isConnected()) {
            availableConnections.offer(client);
        }
    }
    
    /**
     * 关闭连接池
     */
    public void shutdown() {
        logger.info("🛑 正在关闭 WebSocket 连接池...");
        
        AdvancedWebSocketClient client;
        while ((client = availableConnections.poll()) != null) {
            client.shutdown();
        }
        
        logger.info("✅ WebSocket 连接池已关闭");
    }
    
    /**
     * 获取池统计信息
     */
    public PoolStats getStats() {
        return new PoolStats(
            poolSize,
            availableConnections.size(),
            totalConnections.get()
        );
    }
    
    /**
     * 连接池统计信息
     */
    public static class PoolStats {
        private final int poolSize;
        private final int availableConnections;
        private final int totalConnections;
        
        public PoolStats(int poolSize, int availableConnections, int totalConnections) {
            this.poolSize = poolSize;
            this.availableConnections = availableConnections;
            this.totalConnections = totalConnections;
        }
        
        public int getPoolSize() { return poolSize; }
        public int getAvailableConnections() { return availableConnections; }
        public int getTotalConnections() { return totalConnections; }
        public int getUsedConnections() { return poolSize - availableConnections; }
        
        @Override
        public String toString() {
            return String.format("PoolStats{poolSize=%d, available=%d, used=%d, total=%d}", 
                                poolSize, availableConnections, getUsedConnections(), totalConnections);
        }
    }
}
```

### 连接池使用示例

```java
public class ConnectionPoolDemo {
    public static void main(String[] args) throws Exception {
        // 创建连接池配置
        AdvancedWebSocketClient.WSConfig config = new AdvancedWebSocketClient.WSConfig()
            .setAutoReconnect(true)
            .setHeartbeatIntervalMs(30000);
        
        // 创建连接池
        WebSocketConnectionPool pool = new WebSocketConnectionPool("ws://localhost:8080/ws", config, 5);
        
        // 模拟并发使用连接
        for (int i = 0; i < 10; i++) {
            final int taskId = i;
            new Thread(() -> {
                try {
                    // 从池中获取连接
                    AdvancedWebSocketClient client = pool.getConnection();
                    
                    // 使用连接发送消息
                    client.sendText("任务 " + taskId + " 的消息").join();
                    
                    // 归还连接
                    pool.returnConnection(client);
                    
                } catch (Exception e) {
                    e.printStackTrace();
                }
            }).start();
        }
        
        // 等待任务完成
        Thread.sleep(5000);
        
        // 输出池统计信息
        System.out.println("连接池状态: " + pool.getStats());
        
        // 关闭连接池
        pool.shutdown();
    }
}
```

## 🎯 最佳实践

### 1. 错误处理和重试

```java
public class RobustWebSocketClient extends AdvancedWebSocketClient {
    private static final Logger logger = LoggerFactory.getLogger(RobustWebSocketClient.class);
    
    public RobustWebSocketClient(String serverUrl, WSConfig config) {
        super(serverUrl, config);
        setupRobustErrorHandling();
    }
    
    private void setupRobustErrorHandling() {
        this
            .on("connectError", (Throwable error) -> {
                logger.error("连接错误: {}", error.getMessage());
                
                // 根据错误类型采取不同策略
                if (isNetworkError(error)) {
                    logger.info("检测到网络错误，将延迟重连");
                    // 网络错误时增加重连延迟
                } else if (isAuthError(error)) {
                    logger.error("认证失败，停止重连");
                    // 认证错误时停止自动重连
                }
            })
            .on("sendError", (Throwable error) -> {
                logger.warn("发送失败: {}", error.getMessage());
                // 实现发送重试逻辑
                retryFailedMessage(error);
            })
            .on("ackTimeout", (String messageId) -> {
                logger.warn("ACK 超时: {}", messageId);
                // 处理 ACK 超时
            });
    }
    
    private boolean isNetworkError(Throwable error) {
        return error instanceof java.net.ConnectException ||
               error instanceof java.net.SocketTimeoutException;
    }
    
    private boolean isAuthError(Throwable error) {
        return error.getMessage() != null && error.getMessage().contains("401");
    }
    
    private void retryFailedMessage(Throwable error) {
        // 实现消息重试逻辑
    }
}
```

### 2. 性能优化

```java
public class OptimizedWebSocketClient extends AdvancedWebSocketClient {
    
    public OptimizedWebSocketClient(String serverUrl) {
        super(serverUrl, createOptimizedConfig());
    }
    
    private static WSConfig createOptimizedConfig() {
        return new WSConfig()
            .setHeartbeatIntervalMs(60000)     // 降低心跳频率
            .setMessageBufferSize(1024)        // 增大缓冲区
            .setReconnectIntervalMs(1000)      // 快速重连
            .setMaxReconnectIntervalMs(10000); // 限制最大重连间隔
    }
    
    @Override
    public CompletableFuture<Void> sendText(String message) {
        // 消息压缩
        if (message.length() > 1024) {
            message = compressMessage(message);
        }
        
        return super.sendText(message);
    }
    
    private String compressMessage(String message) {
        // 实现消息压缩逻辑
        return message; // 简化示例
    }
}
```

### 3. 监控和指标

```java
@Component
public class WebSocketMetrics {
    private final MeterRegistry meterRegistry;
    private final Counter connectionCounter;
    private final Counter messageCounter;
    private final Gauge connectionGauge;
    
    public WebSocketMetrics(MeterRegistry meterRegistry) {
        this.meterRegistry = meterRegistry;
        this.connectionCounter = Counter.builder("websocket.connections.total")
            .description("Total WebSocket connections")
            .register(meterRegistry);
        this.messageCounter = Counter.builder("websocket.messages.total")
            .description("Total WebSocket messages")
            .register(meterRegistry);
        this.connectionGauge = Gauge.builder("websocket.connections.active")
            .description("Active WebSocket connections")
            .register(meterRegistry, this, WebSocketMetrics::getActiveConnections);
    }
    
    public void incrementConnection() {
        connectionCounter.increment();
    }
    
    public void incrementMessage(String type) {
        messageCounter.increment(Tags.of("type", type));
    }
    
    private double getActiveConnections() {
        // 返回当前活跃连接数
        return 0; // 实际实现需要跟踪连接状态
    }
}
```

### 4. 测试工具

```java
@TestComponent
public class WebSocketTestClient {
    
    public static void loadTest(String serverUrl, int clients, int messagesPerClient) {
        List<AdvancedWebSocketClient> testClients = new ArrayList<>();
        CountDownLatch latch = new CountDownLatch(clients);
        
        for (int i = 0; i < clients; i++) {
            AdvancedWebSocketClient client = new AdvancedWebSocketClient(serverUrl);
            
            client.on("connected", (Void unused) -> {
                latch.countDown();
            });
            
            testClients.add(client);
            client.connect();
        }
        
        try {
            // 等待所有客户端连接
            latch.await(30, TimeUnit.SECONDS);
            
            // 开始发送消息
            long startTime = System.currentTimeMillis();
            
            List<CompletableFuture<Void>> futures = new ArrayList<>();
            for (int i = 0; i < clients; i++) {
                AdvancedWebSocketClient client = testClients.get(i);
                for (int j = 0; j < messagesPerClient; j++) {
                    CompletableFuture<Void> future = client.sendText("测试消息 " + j);
                    futures.add(future);
                }
            }
            
            // 等待所有消息发送完成
            CompletableFuture.allOf(futures.toArray(new CompletableFuture[0])).join();
            
            long endTime = System.currentTimeMillis();
            long duration = endTime - startTime;
            int totalMessages = clients * messagesPerClient;
            
            System.out.println("负载测试完成:");
            System.out.println("客户端数: " + clients);
            System.out.println("总消息数: " + totalMessages);
            System.out.println("总耗时: " + duration + "ms");
            System.out.println("吞吐量: " + (totalMessages * 1000.0 / duration) + " 消息/秒");
            
        } catch (Exception e) {
            e.printStackTrace();
        } finally {
            // 清理资源
            testClients.forEach(AdvancedWebSocketClient::shutdown);
        }
    }
}
```

## 📋 配置参考

### application.yml 配置示例

```yaml
websocket:
  server:
    url: ws://localhost:8080/ws
  client:
    auto-reconnect: true
    max-reconnect-attempts: 10
    reconnect-interval: 2000
    max-reconnect-interval: 30000
    reconnect-backoff-factor: 1.5
    heartbeat-interval: 30000
    message-buffer-size: 256
    max-message-size: 1048576  # 1MB
    connection-timeout: 10000
  pool:
    enabled: true
    size: 5
    max-idle: 3
  monitoring:
    enabled: true
    metrics-interval: 60000
```

### 环境变量配置

```bash
# WebSocket 服务器地址
WEBSOCKET_SERVER_URL=ws://localhost:8080/ws

# 连接配置
WEBSOCKET_AUTO_RECONNECT=true
WEBSOCKET_MAX_RECONNECT_ATTEMPTS=10
WEBSOCKET_HEARTBEAT_INTERVAL=30000

# 池配置
WEBSOCKET_POOL_SIZE=5

# 日志级别
LOGGING_LEVEL_COM_EXAMPLE_WSC=DEBUG
```

这个 Java 客户端实现提供了与 go-wsc 服务端兼容的全部功能，包括自动重连、心跳检测、ACK 确认、消息队列等企业级特性。可以根据具体需求进行定制和扩展。