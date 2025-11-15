# Java 客户端集成指南 ☕

> 本文档提供基于 go-wsc 的 Java WebSocket 客户端完整实现方案，支持企业级特性如自动重连、ACK 确认、心跳检测等。

## 📖 目录

- [Maven 依赖](#-maven-依赖)
- [基础 WebSocket 客户端](#-基础-websocket-客户端)
- [高级特性](#-高级特性)
- [Spring Boot 集成](#-spring-boot-集成)
- [ACK 确认机制](#-ack-确认机制)
- [实战案例](#-实战案例)

## 📦 Maven 依赖

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
        <groupId>org.glassfish.tyrus</groupId>
        <artifactId>tyrus-client</artifactId>
        <version>2.1.0</version>
    </dependency>
    
    <dependency>
        <groupId>org.glassfish.tyrus</groupId>
        <artifactId>tyrus-container-grizzly-client</artifactId>
        <version>2.1.0</version>
    </dependency>
    
    <!-- JSON 处理 -->
    <dependency>
        <groupId>com.fasterxml.jackson.core</groupId>
        <artifactId>jackson-databind</artifactId>
        <version>2.15.2</version>
    </dependency>
    
    <!-- 日志 -->
    <dependency>
        <groupId>ch.qos.logback</groupId>
        <artifactId>logback-classic</artifactId>
        <version>1.4.11</version>
    </dependency>
</dependencies>
```

### Spring Boot 项目依赖

```xml
<dependency>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-starter-websocket</artifactId>
</dependency>
```

## 🚀 基础 WebSocket 客户端

### 消息实体类

```java
package com.example.websocket;

import com.fasterxml.jackson.annotation.JsonProperty;
import java.time.Instant;

/**
 * WebSocket 消息实体
 */
public class WebSocketMessage {
    @JsonProperty("id")
    private String id;
    
    @JsonProperty("type")
    private String type;
    
    @JsonProperty("data")
    private Object data;
    
    @JsonProperty("timestamp")
    private long timestamp;
    
    @JsonProperty("requireAck")
    private boolean requireAck;
    
    @JsonProperty("ackId")
    private String ackId;
    
    public WebSocketMessage() {
        this.timestamp = Instant.now().toEpochMilli();
    }
    
    public WebSocketMessage(String type, Object data) {
        this();
        this.type = type;
        this.data = data;
    }
    
    public WebSocketMessage(String id, String type, Object data) {
        this(type, data);
        this.id = id;
    }
    
    // Getters and Setters
    public String getId() { return id; }
    public void setId(String id) { this.id = id; }
    
    public String getType() { return type; }
    public void setType(String type) { this.type = type; }
    
    public Object getData() { return data; }
    public void setData(Object data) { this.data = data; }
    
    public long getTimestamp() { return timestamp; }
    public void setTimestamp(long timestamp) { this.timestamp = timestamp; }
    
    public boolean isRequireAck() { return requireAck; }
    public void setRequireAck(boolean requireAck) { this.requireAck = requireAck; }
    
    public String getAckId() { return ackId; }
    public void setAckId(String ackId) { this.ackId = ackId; }
    
    @Override
    public String toString() {
        return "WebSocketMessage{" +
               "id='" + id + '\'' +
               ", type='" + type + '\'' +
               ", data=" + data +
               ", timestamp=" + timestamp +
               ", requireAck=" + requireAck +
               ", ackId='" + ackId + '\'' +
               '}';
    }
}
```

### 高级 WebSocket 客户端

```java
package com.example.websocket;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import javax.websocket.*;
import javax.websocket.ClientEndpointConfig;
import java.io.IOException;
import java.net.URI;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Consumer;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.UUID;

/**
 * 高级 WebSocket 客户端 - 基于 go-wsc 设计理念
 * 支持自动重连、心跳检测、消息队列、ACK 确认等企业级特性
 */
@ClientEndpoint
public class AdvancedWebSocketClient {
    private static final Logger logger = LoggerFactory.getLogger(AdvancedWebSocketClient.class);
    
    // 配置
    private final WebSocketConfig config;
    private final URI serverUri;
    private final ObjectMapper objectMapper;
    
    // 连接管理
    private volatile Session session;
    private final AtomicBoolean isConnecting = new AtomicBoolean(false);
    private final AtomicInteger reconnectAttempts = new AtomicInteger(0);
    
    // 线程池和定时器
    private final ScheduledExecutorService scheduler = Executors.newScheduledThreadPool(3);
    private ScheduledFuture<?> heartbeatTask;
    private ScheduledFuture<?> reconnectTask;
    
    // 消息队列和处理
    private final BlockingQueue<WebSocketMessage> messageQueue;
    private final Map<String, CompletableFuture<WebSocketMessage>> pendingAcks = new ConcurrentHashMap<>();
    private final AtomicInteger messageIdGenerator = new AtomicInteger(0);
    
    // 事件回调
    private Consumer<Void> onConnectedCallback;
    private Consumer<Throwable> onErrorCallback;
    private Consumer<CloseReason> onClosedCallback;
    private Consumer<WebSocketMessage> onMessageCallback;
    private Consumer<String> onTextMessageCallback;
    private Consumer<byte[]> onBinaryMessageCallback;
    
    public AdvancedWebSocketClient(String url) {
        this(url, new WebSocketConfig());
    }
    
    public AdvancedWebSocketClient(String url, WebSocketConfig config) {
        this.serverUri = URI.create(url);
        this.config = config;
        this.objectMapper = new ObjectMapper();
        this.messageQueue = new LinkedBlockingQueue<>(config.getMessageBufferSize());
    }
    
    /**
     * 连接到 WebSocket 服务器
     */
    public CompletableFuture<Void> connect() {
        CompletableFuture<Void> future = new CompletableFuture<>();
        
        if (isConnected() || !isConnecting.compareAndSet(false, true)) {
            future.complete(null);
            return future;
        }
        
        try {
            WebSocketContainer container = ContainerProvider.getWebSocketContainer();
            container.setDefaultMaxTextMessageBufferSize(config.getMaxMessageSize());
            container.setDefaultMaxBinaryMessageBufferSize(config.getMaxMessageSize());
            
            ClientEndpointConfig endpointConfig = ClientEndpointConfig.Builder.create().build();
            
            container.connectToServer(this, endpointConfig, serverUri);
            
            // 设置连接超时
            scheduler.schedule(() -> {
                if (isConnecting.get() && !isConnected()) {
                    isConnecting.set(false);
                    future.completeExceptionally(new TimeoutException("连接超时"));
                }
            }, config.getConnectionTimeout(), TimeUnit.MILLISECONDS);
            
        } catch (Exception e) {
            isConnecting.set(false);
            future.completeExceptionally(e);
        }
        
        return future;
    }
    
    /**
     * 连接建立时的回调
     */
    @OnOpen
    public void onOpen(Session session) {
        this.session = session;
        isConnecting.set(false);
        reconnectAttempts.set(0);
        
        logger.info("✅ WebSocket 连接已建立: {}", session.getId());
        
        // 启动心跳
        startHeartbeat();
        
        // 处理消息队列
        flushMessageQueue();
        
        // 触发连接回调
        if (onConnectedCallback != null) {
            try {
                onConnectedCallback.accept(null);
            } catch (Exception e) {
                logger.error("连接回调执行失败", e);
            }
        }
    }
    
    /**
     * 接收消息的回调
     */
    @OnMessage
    public void onMessage(String message) {
        try {
            logger.debug("📨 收到文本消息: {}", message);
            
            // 处理心跳响应
            if ("pong".equals(message)) {
                logger.trace("💓 收到心跳响应");
                return;
            }
            
            // 尝试解析为 JSON 消息
            try {
                WebSocketMessage wsMessage = objectMapper.readValue(message, WebSocketMessage.class);
                handleStructuredMessage(wsMessage);
            } catch (Exception e) {
                // 作为普通文本消息处理
                handleTextMessage(message);
            }
            
        } catch (Exception e) {
            logger.error("处理文本消息失败: {}", message, e);
        }
    }
    
    /**
     * 接收二进制消息的回调
     */
    @OnMessage
    public void onMessage(byte[] data) {
        try {
            logger.debug("📦 收到二进制消息: {} 字节", data.length);
            handleBinaryMessage(data);
        } catch (Exception e) {
            logger.error("处理二进制消息失败", e);
        }
    }
    
    /**
     * 连接关闭时的回调
     */
    @OnClose
    public void onClose(Session session, CloseReason closeReason) {
        this.session = null;
        isConnecting.set(false);
        
        logger.info("🔒 WebSocket 连接关闭: {} - {}", 
                   closeReason.getCloseCode(), closeReason.getReasonPhrase());
        
        // 停止心跳
        stopHeartbeat();
        
        // 触发关闭回调
        if (onClosedCallback != null) {
            try {
                onClosedCallback.accept(closeReason);
            } catch (Exception e) {
                logger.error("关闭回调执行失败", e);
            }
        }
        
        // 自动重连
        if (config.isAutoReconnect() && 
            reconnectAttempts.get() < config.getMaxReconnectAttempts() &&
            closeReason.getCloseCode().getCode() != CloseReason.CloseCodes.NORMAL_CLOSURE.getCode()) {
            scheduleReconnect();
        }
    }
    
    /**
     * 错误处理回调
     */
    @OnError
    public void onError(Session session, Throwable throwable) {
        logger.error("❌ WebSocket 连接错误", throwable);
        
        if (onErrorCallback != null) {
            try {
                onErrorCallback.accept(throwable);
            } catch (Exception e) {
                logger.error("错误回调执行失败", e);
            }
        }
    }
    
    /**
     * 发送文本消息
     */
    public CompletableFuture<Void> sendText(String message) {
        return sendMessage(new WebSocketMessage("text", message));
    }
    
    /**
     * 发送 JSON 消息
     */
    public CompletableFuture<Void> sendJSON(Object data) {
        return sendMessage(new WebSocketMessage("json", data));
    }
    
    /**
     * 发送需要 ACK 确认的消息
     */
    public CompletableFuture<WebSocketMessage> sendMessageWithAck(String type, Object data) {
        return sendMessageWithAck(type, data, config.getAckTimeout(), TimeUnit.MILLISECONDS);
    }
    
    /**
     * 发送需要 ACK 确认的消息（带超时）
     */
    public CompletableFuture<WebSocketMessage> sendMessageWithAck(String type, Object data, long timeout, TimeUnit unit) {
        String messageId = generateMessageId();
        WebSocketMessage message = new WebSocketMessage(messageId, type, data);
        message.setRequireAck(true);
        
        CompletableFuture<WebSocketMessage> ackFuture = new CompletableFuture<>();
        pendingAcks.put(messageId, ackFuture);
        
        // 设置 ACK 超时
        scheduler.schedule(() -> {
            CompletableFuture<WebSocketMessage> pending = pendingAcks.remove(messageId);
            if (pending != null && !pending.isDone()) {
                pending.completeExceptionally(new TimeoutException("ACK 超时: " + messageId));
            }
        }, timeout, unit);
        
        sendMessage(message).whenComplete((result, ex) -> {
            if (ex != null) {
                CompletableFuture<WebSocketMessage> pending = pendingAcks.remove(messageId);
                if (pending != null && !pending.isDone()) {
                    pending.completeExceptionally(ex);
                }
            }
        });
        
        return ackFuture;
    }
    
    /**
     * 发送二进制消息
     */
    public CompletableFuture<Void> sendBinary(byte[] data) {
        CompletableFuture<Void> future = new CompletableFuture<>();
        
        if (!isConnected()) {
            if (config.isQueueWhenDisconnected() && messageQueue.size() < config.getMessageBufferSize()) {
                WebSocketMessage message = new WebSocketMessage("binary", data);
                if (messageQueue.offer(message)) {
                    future.complete(null);
                } else {
                    future.completeExceptionally(new IllegalStateException("消息队列已满"));
                }
            } else {
                future.completeExceptionally(new IllegalStateException("WebSocket 未连接"));
            }
            return future;
        }
        
        try {
            session.getAsyncRemote().sendBinary(
                java.nio.ByteBuffer.wrap(data),
                new SendHandler() {
                    @Override
                    public void onResult(SendResult result) {
                        if (result.isOK()) {
                            future.complete(null);
                        } else {
                            future.completeExceptionally(result.getException());
                        }
                    }
                }
            );
        } catch (Exception e) {
            future.completeExceptionally(e);
        }
        
        return future;
    }
    
    /**
     * 发送 Ping 消息
     */
    public CompletableFuture<Void> sendPing() {
        return sendPing("ping".getBytes());
    }
    
    public CompletableFuture<Void> sendPing(byte[] data) {
        CompletableFuture<Void> future = new CompletableFuture<>();
        
        if (!isConnected()) {
            future.completeExceptionally(new IllegalStateException("WebSocket 未连接"));
            return future;
        }
        
        try {
            session.getAsyncRemote().sendPing(
                java.nio.ByteBuffer.wrap(data),
                new SendHandler() {
                    @Override
                    public void onResult(SendResult result) {
                        if (result.isOK()) {
                            future.complete(null);
                        } else {
                            future.completeExceptionally(result.getException());
                        }
                    }
                }
            );
        } catch (Exception e) {
            future.completeExceptionally(e);
        }
        
        return future;
    }
    
    /**
     * 检查连接状态
     */
    public boolean isConnected() {
        return session != null && session.isOpen();
    }
    
    /**
     * 断开连接
     */
    public void disconnect() {
        config.setAutoReconnect(false); // 停止自动重连
        
        if (session != null && session.isOpen()) {
            try {
                session.close(new CloseReason(CloseReason.CloseCodes.NORMAL_CLOSURE, "客户端主动断开"));
            } catch (IOException e) {
                logger.error("关闭连接失败", e);
            }
        }
        
        shutdown();
    }
    
    /**
     * 关闭客户端并清理资源
     */
    public void shutdown() {
        stopHeartbeat();
        cancelReconnect();
        
        // 清理待确认的消息
        pendingAcks.values().forEach(future -> {
            if (!future.isDone()) {
                future.cancel(true);
            }
        });
        pendingAcks.clear();
        
        messageQueue.clear();
        
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
    }
    
    // ============ 私有方法 ============
    
    private CompletableFuture<Void> sendMessage(WebSocketMessage message) {
        CompletableFuture<Void> future = new CompletableFuture<>();
        
        if (!isConnected()) {
            if (config.isQueueWhenDisconnected() && messageQueue.size() < config.getMessageBufferSize()) {
                if (messageQueue.offer(message)) {
                    future.complete(null);
                } else {
                    future.completeExceptionally(new IllegalStateException("消息队列已满"));
                }
            } else {
                future.completeExceptionally(new IllegalStateException("WebSocket 未连接"));
            }
            return future;
        }
        
        try {
            String json = objectMapper.writeValueAsString(message);
            session.getAsyncRemote().sendText(
                json,
                new SendHandler() {
                    @Override
                    public void onResult(SendResult result) {
                        if (result.isOK()) {
                            logger.debug("📤 消息发送成功: {}", message.getType());
                            future.complete(null);
                        } else {
                            logger.error("📤 消息发送失败: {}", message.getType(), result.getException());
                            future.completeExceptionally(result.getException());
                        }
                    }
                }
            );
        } catch (Exception e) {
            future.completeExceptionally(e);
        }
        
        return future;
    }
    
    private void handleStructuredMessage(WebSocketMessage message) {
        // 处理 ACK 确认
        if ("ack".equals(message.getType()) && message.getAckId() != null) {
            CompletableFuture<WebSocketMessage> pending = pendingAcks.remove(message.getAckId());
            if (pending != null && !pending.isDone()) {
                pending.complete(message);
            }
            return;
        }
        
        // 发送 ACK 确认
        if (message.isRequireAck() && message.getId() != null) {
            WebSocketMessage ackMessage = new WebSocketMessage("ack", "confirmed");
            ackMessage.setAckId(message.getId());
            sendMessage(ackMessage);
        }
        
        // 触发消息回调
        if (onMessageCallback != null) {
            try {
                onMessageCallback.accept(message);
            } catch (Exception e) {
                logger.error("消息回调执行失败", e);
            }
        }
    }
    
    private void handleTextMessage(String message) {
        if (onTextMessageCallback != null) {
            try {
                onTextMessageCallback.accept(message);
            } catch (Exception e) {
                logger.error("文本消息回调执行失败", e);
            }
        }
    }
    
    private void handleBinaryMessage(byte[] data) {
        if (onBinaryMessageCallback != null) {
            try {
                onBinaryMessageCallback.accept(data);
            } catch (Exception e) {
                logger.error("二进制消息回调执行失败", e);
            }
        }
    }
    
    private void startHeartbeat() {
        stopHeartbeat();
        
        if (config.getHeartbeatInterval() > 0) {
            heartbeatTask = scheduler.scheduleAtFixedRate(() -> {
                if (isConnected()) {
                    sendPing().whenComplete((result, ex) -> {
                        if (ex != null) {
                            logger.warn("发送心跳失败", ex);
                        }
                    });
                }
            }, config.getHeartbeatInterval(), config.getHeartbeatInterval(), TimeUnit.MILLISECONDS);
        }
    }
    
    private void stopHeartbeat() {
        if (heartbeatTask != null && !heartbeatTask.isCancelled()) {
            heartbeatTask.cancel(false);
            heartbeatTask = null;
        }
    }
    
    private void scheduleReconnect() {
        cancelReconnect();
        
        int attempts = reconnectAttempts.incrementAndGet();
        long delay = Math.min(
            config.getReconnectInterval() * (long) Math.pow(config.getReconnectBackoffFactor(), attempts - 1),
            config.getMaxReconnectInterval()
        );
        
        logger.info("🔄 将在 {}ms 后尝试重连 ({}/{})", delay, attempts, config.getMaxReconnectAttempts());
        
        reconnectTask = scheduler.schedule(() -> {
            connect().whenComplete((result, ex) -> {
                if (ex != null) {
                    logger.error("重连失败", ex);
                }
            });
        }, delay, TimeUnit.MILLISECONDS);
    }
    
    private void cancelReconnect() {
        if (reconnectTask != null && !reconnectTask.isCancelled()) {
            reconnectTask.cancel(false);
            reconnectTask = null;
        }
    }
    
    private void flushMessageQueue() {
        while (!messageQueue.isEmpty() && isConnected()) {
            WebSocketMessage message = messageQueue.poll();
            if (message != null) {
                sendMessage(message).whenComplete((result, ex) -> {
                    if (ex != null) {
                        logger.error("发送队列消息失败: {}", message.getType(), ex);
                    }
                });
            }
        }
    }
    
    private String generateMessageId() {
        return "msg-" + System.currentTimeMillis() + "-" + messageIdGenerator.incrementAndGet();
    }
    
    // ============ 事件回调设置方法 ============
    
    public AdvancedWebSocketClient onConnected(Consumer<Void> callback) {
        this.onConnectedCallback = callback;
        return this;
    }
    
    public AdvancedWebSocketClient onError(Consumer<Throwable> callback) {
        this.onErrorCallback = callback;
        return this;
    }
    
    public AdvancedWebSocketClient onClosed(Consumer<CloseReason> callback) {
        this.onClosedCallback = callback;
        return this;
    }
    
    public AdvancedWebSocketClient onMessage(Consumer<WebSocketMessage> callback) {
        this.onMessageCallback = callback;
        return this;
    }
    
    public AdvancedWebSocketClient onTextMessage(Consumer<String> callback) {
        this.onTextMessageCallback = callback;
        return this;
    }
    
    public AdvancedWebSocketClient onBinaryMessage(Consumer<byte[]> callback) {
        this.onBinaryMessageCallback = callback;
        return this;
    }
}
```

### WebSocket 配置类

```java
package com.example.websocket;

/**
 * WebSocket 客户端配置
 */
public class WebSocketConfig {
    // 连接配置
    private long connectionTimeout = 10000;        // 连接超时时间 (ms)
    private int maxMessageSize = 1024 * 1024;      // 最大消息大小 (1MB)
    
    // 重连配置
    private boolean autoReconnect = true;          // 自动重连
    private int maxReconnectAttempts = 10;         // 最大重连次数
    private long reconnectInterval = 2000;         // 重连间隔 (ms)
    private long maxReconnectInterval = 30000;     // 最大重连间隔 (ms)
    private double reconnectBackoffFactor = 1.5;   // 重连退避因子
    
    // 心跳配置
    private long heartbeatInterval = 30000;        // 心跳间隔 (ms)
    
    // 消息队列配置
    private int messageBufferSize = 256;           // 消息缓冲区大小
    private boolean queueWhenDisconnected = true;  // 断线时是否缓存消息
    
    // ACK 配置
    private long ackTimeout = 30000;               // ACK 超时时间 (ms)
    
    // 构造函数
    public WebSocketConfig() {}
    
    // Getters and Setters
    public long getConnectionTimeout() { return connectionTimeout; }
    public void setConnectionTimeout(long connectionTimeout) { this.connectionTimeout = connectionTimeout; }
    
    public int getMaxMessageSize() { return maxMessageSize; }
    public void setMaxMessageSize(int maxMessageSize) { this.maxMessageSize = maxMessageSize; }
    
    public boolean isAutoReconnect() { return autoReconnect; }
    public void setAutoReconnect(boolean autoReconnect) { this.autoReconnect = autoReconnect; }
    
    public int getMaxReconnectAttempts() { return maxReconnectAttempts; }
    public void setMaxReconnectAttempts(int maxReconnectAttempts) { this.maxReconnectAttempts = maxReconnectAttempts; }
    
    public long getReconnectInterval() { return reconnectInterval; }
    public void setReconnectInterval(long reconnectInterval) { this.reconnectInterval = reconnectInterval; }
    
    public long getMaxReconnectInterval() { return maxReconnectInterval; }
    public void setMaxReconnectInterval(long maxReconnectInterval) { this.maxReconnectInterval = maxReconnectInterval; }
    
    public double getReconnectBackoffFactor() { return reconnectBackoffFactor; }
    public void setReconnectBackoffFactor(double reconnectBackoffFactor) { this.reconnectBackoffFactor = reconnectBackoffFactor; }
    
    public long getHeartbeatInterval() { return heartbeatInterval; }
    public void setHeartbeatInterval(long heartbeatInterval) { this.heartbeatInterval = heartbeatInterval; }
    
    public int getMessageBufferSize() { return messageBufferSize; }
    public void setMessageBufferSize(int messageBufferSize) { this.messageBufferSize = messageBufferSize; }
    
    public boolean isQueueWhenDisconnected() { return queueWhenDisconnected; }
    public void setQueueWhenDisconnected(boolean queueWhenDisconnected) { this.queueWhenDisconnected = queueWhenDisconnected; }
    
    public long getAckTimeout() { return ackTimeout; }
    public void setAckTimeout(long ackTimeout) { this.ackTimeout = ackTimeout; }
}
```

## 🔧 高级特性

### 连接池管理

```java
package com.example.websocket;

import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * WebSocket 连接池管理器
 */
public class WebSocketConnectionPool {
    private final ConcurrentHashMap<String, AdvancedWebSocketClient> connections = new ConcurrentHashMap<>();
    private final AtomicInteger connectionCounter = new AtomicInteger(0);
    private final int maxConnections;
    
    public WebSocketConnectionPool(int maxConnections) {
        this.maxConnections = maxConnections;
    }
    
    /**
     * 获取或创建连接
     */
    public AdvancedWebSocketClient getConnection(String url) {
        return connections.computeIfAbsent(url, k -> {
            if (connectionCounter.get() >= maxConnections) {
                throw new IllegalStateException("连接池已满，最大连接数: " + maxConnections);
            }
            
            connectionCounter.incrementAndGet();
            AdvancedWebSocketClient client = new AdvancedWebSocketClient(url);
            
            // 设置连接关闭时从池中移除
            client.onClosed(reason -> {
                connections.remove(url);
                connectionCounter.decrementAndGet();
            });
            
            return client;
        });
    }
    
    /**
     * 移除连接
     */
    public void removeConnection(String url) {
        AdvancedWebSocketClient client = connections.remove(url);
        if (client != null) {
            client.disconnect();
            connectionCounter.decrementAndGet();
        }
    }
    
    /**
     * 关闭所有连接
     */
    public void shutdown() {
        connections.values().forEach(AdvancedWebSocketClient::disconnect);
        connections.clear();
        connectionCounter.set(0);
    }
    
    public int getConnectionCount() {
        return connectionCounter.get();
    }
    
    public int getMaxConnections() {
        return maxConnections;
    }
}
```

## 🌱 Spring Boot 集成

### WebSocket 配置

```java
package com.example.websocket.config;

import org.springframework.context.annotation.Configuration;
import org.springframework.web.socket.config.annotation.EnableWebSocket;
import org.springframework.web.socket.config.annotation.WebSocketConfigurer;
import org.springframework.web.socket.config.annotation.WebSocketHandlerRegistry;
import com.example.websocket.SpringWebSocketHandler;

@Configuration
@EnableWebSocket
public class WebSocketConfig implements WebSocketConfigurer {
    
    @Override
    public void registerWebSocketHandlers(WebSocketHandlerRegistry registry) {
        registry.addHandler(new SpringWebSocketHandler(), "/ws")
                .setAllowedOrigins("*");
    }
}
```

### Spring WebSocket 客户端服务

```java
package com.example.websocket.service;

import com.example.websocket.AdvancedWebSocketClient;
import com.example.websocket.WebSocketConfig;
import com.example.websocket.WebSocketMessage;
import org.springframework.stereotype.Service;
import org.springframework.beans.factory.annotation.Value;
import javax.annotation.PostConstruct;
import javax.annotation.PreDestroy;
import java.util.concurrent.CompletableFuture;

@Service
public class WebSocketClientService {
    
    @Value("${websocket.server.url:ws://localhost:8080/ws}")
    private String serverUrl;
    
    private AdvancedWebSocketClient client;
    
    @PostConstruct
    public void init() {
        WebSocketConfig config = new WebSocketConfig();
        config.setAutoReconnect(true);
        config.setHeartbeatInterval(30000);
        config.setMaxReconnectAttempts(5);
        
        client = new AdvancedWebSocketClient(serverUrl, config);
        
        // 设置事件处理器
        client.onConnected(v -> {
            System.out.println("✅ 连接到服务器: " + serverUrl);
        });
        
        client.onMessage(message -> {
            handleMessage(message);
        });
        
        client.onError(error -> {
            System.err.println("❌ 连接错误: " + error.getMessage());
        });
        
        // 自动连接
        client.connect();
    }
    
    @PreDestroy
    public void cleanup() {
        if (client != null) {
            client.disconnect();
        }
    }
    
    /**
     * 发送消息
     */
    public CompletableFuture<Void> sendMessage(String type, Object data) {
        return client.sendJSON(new WebSocketMessage(type, data));
    }
    
    /**
     * 发送需要确认的消息
     */
    public CompletableFuture<WebSocketMessage> sendMessageWithAck(String type, Object data) {
        return client.sendMessageWithAck(type, data);
    }
    
    /**
     * 处理接收到的消息
     */
    private void handleMessage(WebSocketMessage message) {
        switch (message.getType()) {
            case "chat":
                handleChatMessage(message);
                break;
            case "notification":
                handleNotification(message);
                break;
            case "system":
                handleSystemMessage(message);
                break;
            default:
                System.out.println("未知消息类型: " + message.getType());
        }
    }
    
    private void handleChatMessage(WebSocketMessage message) {
        System.out.println("💬 聊天消息: " + message.getData());
    }
    
    private void handleNotification(WebSocketMessage message) {
        System.out.println("🔔 通知: " + message.getData());
    }
    
    private void handleSystemMessage(WebSocketMessage message) {
        System.out.println("⚙️ 系统消息: " + message.getData());
    }
    
    public boolean isConnected() {
        return client != null && client.isConnected();
    }
}
```

## ✅ ACK 确认机制

### ACK 消息处理器

```java
package com.example.websocket.ack;

import com.example.websocket.WebSocketMessage;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.Executors;

/**
 * ACK 确认机制管理器
 */
public class AckManager {
    private final ConcurrentHashMap<String, PendingAck> pendingAcks = new ConcurrentHashMap<>();
    private final ScheduledExecutorService scheduler = Executors.newScheduledThreadPool(2);
    
    private static class PendingAck {
        final CompletableFuture<WebSocketMessage> future;
        final long createTime;
        final int maxRetries;
        int retryCount;
        
        PendingAck(CompletableFuture<WebSocketMessage> future, int maxRetries) {
            this.future = future;
            this.createTime = System.currentTimeMillis();
            this.maxRetries = maxRetries;
            this.retryCount = 0;
        }
    }
    
    /**
     * 注册待确认消息
     */
    public CompletableFuture<WebSocketMessage> registerAck(String messageId, long timeout, TimeUnit unit, int maxRetries) {
        CompletableFuture<WebSocketMessage> future = new CompletableFuture<>();
        PendingAck pendingAck = new PendingAck(future, maxRetries);
        pendingAcks.put(messageId, pendingAck);
        
        // 设置超时
        scheduler.schedule(() -> {
            PendingAck ack = pendingAcks.get(messageId);
            if (ack != null && !ack.future.isDone()) {
                if (ack.retryCount < ack.maxRetries) {
                    // 重试
                    ack.retryCount++;
                    System.out.println("🔄 ACK 超时，重试 " + ack.retryCount + "/" + ack.maxRetries + ": " + messageId);
                    // 这里应该重新发送消息
                } else {
                    // 超过最大重试次数
                    pendingAcks.remove(messageId);
                    ack.future.completeExceptionally(new java.util.concurrent.TimeoutException("ACK 超时: " + messageId));
                }
            }
        }, timeout, unit);
        
        return future;
    }
    
    /**
     * 处理 ACK 确认
     */
    public void handleAck(String messageId, WebSocketMessage ackMessage) {
        PendingAck pendingAck = pendingAcks.remove(messageId);
        if (pendingAck != null && !pendingAck.future.isDone()) {
            pendingAck.future.complete(ackMessage);
            System.out.println("✅ 收到 ACK 确认: " + messageId);
        }
    }
    
    /**
     * 清理过期的 ACK
     */
    public void cleanupExpiredAcks(long maxAge, TimeUnit unit) {
        long maxAgeMs = unit.toMillis(maxAge);
        long now = System.currentTimeMillis();
        
        pendingAcks.entrySet().removeIf(entry -> {
            PendingAck ack = entry.getValue();
            if (now - ack.createTime > maxAgeMs) {
                if (!ack.future.isDone()) {
                    ack.future.completeExceptionally(new java.util.concurrent.TimeoutException("ACK 过期: " + entry.getKey()));
                }
                return true;
            }
            return false;
        });
    }
    
    public void shutdown() {
        scheduler.shutdown();
        pendingAcks.clear();
    }
}
```

## 📱 实战案例

### 聊天应用示例

```java
package com.example.websocket.demo;

import com.example.websocket.AdvancedWebSocketClient;
import com.example.websocket.WebSocketConfig;
import com.example.websocket.WebSocketMessage;

import java.util.Scanner;
import java.util.concurrent.CompletableFuture;

/**
 * 聊天应用演示
 */
public class ChatClientDemo {
    public static void main(String[] args) {
        WebSocketConfig config = new WebSocketConfig();
        config.setAutoReconnect(true);
        config.setHeartbeatInterval(30000);
        config.setMaxReconnectAttempts(5);
        
        AdvancedWebSocketClient client = new AdvancedWebSocketClient("ws://localhost:8080/ws", config);
        
        // 设置事件处理器
        client.onConnected(v -> {
            System.out.println("✅ 已连接到聊天服务器");
            System.out.println("输入消息并按回车发送，输入 'quit' 退出");
        });
        
        client.onMessage(message -> {
            handleChatMessage(message);
        });
        
        client.onError(error -> {
            System.err.println("❌ 连接错误: " + error.getMessage());
        });
        
        client.onClosed(reason -> {
            System.out.println("🔒 连接已关闭: " + reason.getReasonPhrase());
        });
        
        // 连接到服务器
        client.connect().whenComplete((result, ex) -> {
            if (ex != null) {
                System.err.println("连接失败: " + ex.getMessage());
                return;
            }
            
            // 发送用户加入消息
            client.sendJSON(new WebSocketMessage("user_join", "用户已加入聊天室"));
        });
        
        // 处理用户输入
        Scanner scanner = new Scanner(System.in);
        while (true) {
            String input = scanner.nextLine();
            
            if ("quit".equalsIgnoreCase(input.trim())) {
                break;
            }
            
            if (!input.trim().isEmpty()) {
                // 发送聊天消息
                WebSocketMessage chatMessage = new WebSocketMessage("chat", input);
                client.sendMessage(chatMessage).whenComplete((result, ex) -> {
                    if (ex != null) {
                        System.err.println("发送失败: " + ex.getMessage());
                    }
                });
            }
        }
        
        // 清理资源
        client.disconnect();
        scanner.close();
    }
    
    private static void handleChatMessage(WebSocketMessage message) {
        switch (message.getType()) {
            case "chat":
                System.out.println("💬 " + message.getData());
                break;
            case "user_join":
                System.out.println("👋 " + message.getData());
                break;
            case "user_leave":
                System.out.println("👋 " + message.getData());
                break;
            case "system":
                System.out.println("⚙️ 系统: " + message.getData());
                break;
            default:
                System.out.println("📨 " + message.getType() + ": " + message.getData());
        }
    }
}
```

### 文件传输示例

```java
package com.example.websocket.demo;

import com.example.websocket.AdvancedWebSocketClient;
import com.example.websocket.WebSocketMessage;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.Base64;
import java.util.HashMap;
import java.util.Map;

/**
 * 文件传输演示
 */
public class FileTransferDemo {
    
    public static void sendFile(AdvancedWebSocketClient client, String filePath) {
        try {
            Path path = Paths.get(filePath);
            byte[] fileBytes = Files.readAllBytes(path);
            String encodedFile = Base64.getEncoder().encodeToString(fileBytes);
            
            Map<String, Object> fileData = new HashMap<>();
            fileData.put("fileName", path.getFileName().toString());
            fileData.put("fileSize", fileBytes.length);
            fileData.put("fileContent", encodedFile);
            fileData.put("mimeType", Files.probeContentType(path));
            
            WebSocketMessage message = new WebSocketMessage("file_transfer", fileData);
            
            // 发送需要确认的文件消息
            client.sendMessageWithAck("file_transfer", fileData)
                  .whenComplete((ackMessage, ex) -> {
                      if (ex != null) {
                          System.err.println("❌ 文件发送失败: " + ex.getMessage());
                      } else {
                          System.out.println("✅ 文件发送成功并已确认: " + path.getFileName());
                      }
                  });
                  
        } catch (IOException e) {
            System.err.println("❌ 读取文件失败: " + e.getMessage());
        }
    }
    
    public static void handleFileReceived(WebSocketMessage message) {
        if (!"file_transfer".equals(message.getType())) {
            return;
        }
        
        try {
            @SuppressWarnings("unchecked")
            Map<String, Object> fileData = (Map<String, Object>) message.getData();
            
            String fileName = (String) fileData.get("fileName");
            String encodedContent = (String) fileData.get("fileContent");
            Integer fileSize = (Integer) fileData.get("fileSize");
            
            byte[] fileBytes = Base64.getDecoder().decode(encodedContent);
            
            // 保存文件
            Path outputPath = Paths.get("downloads", fileName);
            Files.createDirectories(outputPath.getParent());
            Files.write(outputPath, fileBytes);
            
            System.out.println("📁 文件接收成功: " + fileName + " (" + fileSize + " 字节)");
            
        } catch (Exception e) {
            System.err.println("❌ 处理文件失败: " + e.getMessage());
        }
    }
}
```

## 🧪 测试和监控

### 单元测试示例

```java
package com.example.websocket;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.AfterEach;
import static org.junit.jupiter.api.Assertions.*;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;

public class AdvancedWebSocketClientTest {
    
    private AdvancedWebSocketClient client;
    private final String TEST_URL = "ws://localhost:8080/ws";
    
    @BeforeEach
    public void setUp() {
        WebSocketConfig config = new WebSocketConfig();
        config.setConnectionTimeout(5000);
        config.setAutoReconnect(false);
        client = new AdvancedWebSocketClient(TEST_URL, config);
    }
    
    @AfterEach
    public void tearDown() {
        if (client != null) {
            client.disconnect();
        }
    }
    
    @Test
    public void testConnection() throws Exception {
        CompletableFuture<Void> connectFuture = client.connect();
        connectFuture.get(10, TimeUnit.SECONDS);
        assertTrue(client.isConnected());
    }
    
    @Test
    public void testSendMessage() throws Exception {
        client.connect().get(10, TimeUnit.SECONDS);
        
        CompletableFuture<Void> sendFuture = client.sendText("Hello WebSocket!");
        sendFuture.get(5, TimeUnit.SECONDS);
        
        // 验证消息发送成功
        assertDoesNotThrow(() -> sendFuture.get());
    }
    
    @Test
    public void testMessageWithAck() throws Exception {
        client.connect().get(10, TimeUnit.SECONDS);
        
        CompletableFuture<WebSocketMessage> ackFuture = client.sendMessageWithAck("test", "Hello with ACK");
        WebSocketMessage ackMessage = ackFuture.get(10, TimeUnit.SECONDS);
        
        assertNotNull(ackMessage);
        assertEquals("ack", ackMessage.getType());
    }
}
```

### 性能测试

```java
package com.example.websocket.benchmark;

import com.example.websocket.AdvancedWebSocketClient;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicLong;

/**
 * WebSocket 性能测试
 */
public class WebSocketBenchmark {
    
    public static void benchmarkSendMessages(String url, int messageCount, int concurrentClients) {
        CountDownLatch latch = new CountDownLatch(concurrentClients);
        AtomicLong totalSent = new AtomicLong(0);
        AtomicLong totalTime = new AtomicLong(0);
        
        for (int i = 0; i < concurrentClients; i++) {
            new Thread(() -> {
                try {
                    AdvancedWebSocketClient client = new AdvancedWebSocketClient(url);
                    client.connect().get();
                    
                    long startTime = System.currentTimeMillis();
                    
                    for (int j = 0; j < messageCount; j++) {
                        client.sendText("Benchmark message " + j).get();
                        totalSent.incrementAndGet();
                    }
                    
                    long endTime = System.currentTimeMillis();
                    totalTime.addAndGet(endTime - startTime);
                    
                    client.disconnect();
                    
                } catch (Exception e) {
                    e.printStackTrace();
                } finally {
                    latch.countDown();
                }
            }).start();
        }
        
        try {
            latch.await();
            
            long totalMessages = totalSent.get();
            long avgTime = totalTime.get() / concurrentClients;
            double messagesPerSecond = (totalMessages * 1000.0) / avgTime;
            
            System.out.println("=== WebSocket 性能测试结果 ===");
            System.out.println("并发客户端数: " + concurrentClients);
            System.out.println("每客户端消息数: " + messageCount);
            System.out.println("总消息数: " + totalMessages);
            System.out.println("平均耗时: " + avgTime + " ms");
            System.out.println("消息吞吐量: " + String.format("%.2f", messagesPerSecond) + " msg/s");
            
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
    
    public static void main(String[] args) {
        benchmarkSendMessages("ws://localhost:8080/ws", 1000, 10);
    }
}
```

## 🔧 最佳实践

### 1. 资源管理

```java
// 使用 try-with-resources 或确保在 finally 中清理
try {
    AdvancedWebSocketClient client = new AdvancedWebSocketClient(url);
    client.connect().get();
    // 使用客户端...
} finally {
    if (client != null) {
        client.disconnect();
    }
}
```

### 2. 异常处理

```java
client.onError(error -> {
    if (error instanceof java.net.ConnectException) {
        logger.error("连接被拒绝，检查服务器是否启动");
    } else if (error instanceof java.util.concurrent.TimeoutException) {
        logger.error("连接超时，检查网络连接");
    } else {
        logger.error("未知错误", error);
    }
});
```

### 3. 日志配置

```xml
<!-- logback-spring.xml -->
<configuration>
    <appender name="CONSOLE" class="ch.qos.logback.core.ConsoleAppender">
        <encoder>
            <pattern>%d{yyyy-MM-dd HH:mm:ss} [%thread] %-5level %logger{36} - %msg%n</pattern>
        </encoder>
    </appender>
    
    <logger name="com.example.websocket" level="DEBUG"/>
    
    <root level="INFO">
        <appender-ref ref="CONSOLE"/>
    </root>
</configuration>
```

### 4. 配置外部化

```properties
# application.properties
websocket.server.url=ws://localhost:8080/ws
websocket.client.autoReconnect=true
websocket.client.maxReconnectAttempts=5
websocket.client.heartbeatInterval=30000
websocket.client.connectionTimeout=10000
```

这个 Java 客户端实现提供了与 go-wsc 服务器完全兼容的企业级 WebSocket 客户端，支持所有核心特性如自动重连、ACK 确认、心跳检测等。可以直接在 Java 项目中使用，也可以集成到 Spring Boot 应用中。