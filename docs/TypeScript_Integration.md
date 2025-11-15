# TypeScript 前端集成指南 🎯

> 本文档提供 go-wsc 与主流前端框架的完整集成方案，包含实际项目中的最佳实践。

## 📖 目录

- [基础 TypeScript 客户端](#-基础-typescript-客户端)
- [React 集成](#-react-集成)
- [Vue.js 集成](#-vuejs-集成)
- [Angular 集成](#-angular-集成)
- [状态管理集成](#-状态管理集成)
- [实战案例](#-实战案例)

## 🚀 基础 TypeScript 客户端

### 高级 WebSocket 客户端类

```typescript
/**
 * 高级 WebSocket 客户端 - 基于 go-wsc 设计理念
 * 支持自动重连、心跳检测、消息队列等企业级特性
 */

interface WSConfig {
  autoReconnect: boolean;
  maxReconnectAttempts: number;
  reconnectInterval: number;
  maxReconnectInterval: number;
  reconnectBackoffFactor: number;
  heartbeatInterval: number;
  messageBufferSize: number;
  maxMessageSize: number;
  timeout: number;
  protocols: string[];
}

interface WSMessage {
  id?: string;
  type: string;
  data: any;
  timestamp?: number;
}

class AdvancedWebSocketClient {
  private ws: WebSocket | null = null;
  private config: WSConfig;
  private reconnectAttempts: number = 0;
  private reconnectTimer: number | null = null;
  private heartbeatTimer: number | null = null;
  private messageQueue: Array<WSMessage> = [];
  private isConnecting: boolean = false;
  private messageId: number = 0;
  
  // 事件回调存储
  private callbacks: Map<string, Array<(...args: any[]) => void>> = new Map();
  
  constructor(private url: string, config: Partial<WSConfig> = {}) {
    this.config = {
      autoReconnect: true,
      maxReconnectAttempts: 10,
      reconnectInterval: 2000,
      maxReconnectInterval: 30000,
      reconnectBackoffFactor: 1.5,
      heartbeatInterval: 30000,
      messageBufferSize: 256,
      maxMessageSize: 1024 * 1024, // 1MB
      timeout: 10000,
      protocols: [],
      ...config
    };
    
    this.initEventTypes();
  }
  
  private initEventTypes(): void {
    const events = [
      'connected', 'disconnected', 'connectError', 'message', 
      'binaryMessage', 'messageSent', 'sendError', 'close', 
      'ping', 'pong', 'reconnecting', 'messageQueued'
    ];
    events.forEach(event => this.callbacks.set(event, []));
  }
  
  /**
   * 建立连接
   */
  public async connect(): Promise<void> {
    return new Promise((resolve, reject) => {
      if (this.isConnecting || this.isConnected()) {
        resolve();
        return;
      }
      
      this.isConnecting = true;
      this.emit('reconnecting', this.reconnectAttempts);
      
      try {
        this.ws = new WebSocket(this.url, this.config.protocols);
        this.setupEventHandlers(resolve, reject);
        
        // 连接超时处理
        setTimeout(() => {
          if (this.isConnecting) {
            this.isConnecting = false;
            const error = new Error('连接超时');
            this.emit('connectError', error);
            reject(error);
            this.ws?.close();
          }
        }, this.config.timeout);
        
      } catch (error) {
        this.isConnecting = false;
        this.emit('connectError', error);
        reject(error);
      }
    });
  }
  
  /**
   * 设置事件处理器
   */
  private setupEventHandlers(resolve: () => void, reject: (error: Error) => void): void {
    if (!this.ws) return;
    
    this.ws.onopen = () => {
      this.isConnecting = false;
      this.reconnectAttempts = 0;
      
      console.log('✅ WebSocket 连接已建立');
      this.emit('connected');
      
      // 开始心跳
      this.startHeartbeat();
      
      // 发送队列中的消息
      this.flushMessageQueue();
      
      resolve();
    };
    
    this.ws.onmessage = (event) => {
      this.handleIncomingMessage(event);
    };
    
    this.ws.onerror = (error) => {
      console.error('❌ WebSocket 错误:', error);
      this.isConnecting = false;
      const wsError = new Error('WebSocket 连接错误');
      this.emit('connectError', wsError);
      reject(wsError);
    };
    
    this.ws.onclose = (event) => {
      this.isConnecting = false;
      this.stopHeartbeat();
      
      console.log(`🔒 WebSocket 连接关闭: code=${event.code}, reason=${event.reason}`);
      this.emit('close', event.code, event.reason);
      this.emit('disconnected', new Error(`连接关闭: ${event.reason}`));
      
      // 自动重连
      if (this.config.autoReconnect && this.reconnectAttempts < this.config.maxReconnectAttempts) {
        this.scheduleReconnect();
      }
    };
  }
  
  /**
   * 处理接收到的消息
   */
  private handleIncomingMessage(event: MessageEvent): void {
    try {
      if (typeof event.data === 'string') {
        // 处理心跳响应
        if (event.data === 'pong') {
          this.emit('pong', event.data);
          return;
        }
        
        // 尝试解析 JSON 消息
        try {
          const message: WSMessage = JSON.parse(event.data);
          this.emit('message', message);
        } catch {
          // 普通文本消息
          this.emit('message', { type: 'text', data: event.data });
        }
      } else if (event.data instanceof ArrayBuffer) {
        this.emit('binaryMessage', new Uint8Array(event.data));
      } else if (event.data instanceof Blob) {
        event.data.arrayBuffer().then(buffer => {
          this.emit('binaryMessage', new Uint8Array(buffer));
        });
      }
    } catch (error) {
      console.error('处理消息时出错:', error);
    }
  }
  
  /**
   * 发送 JSON 消息
   */
  public async sendMessage(type: string, data: any, needsId: boolean = true): Promise<string | void> {
    const message: WSMessage = {
      type,
      data,
      timestamp: Date.now()
    };
    
    if (needsId) {
      message.id = this.generateMessageId();
    }
    
    return this.sendJSON(message);
  }
  
  /**
   * 发送 JSON 对象
   */
  public async sendJSON(obj: WSMessage): Promise<string | void> {
    try {
      const message = JSON.stringify(obj);
      await this.sendText(message);
      return obj.id;
    } catch (error) {
      throw new Error(`JSON 序列化失败: ${error}`);
    }
  }
  
  /**
   * 发送文本消息
   */
  public async sendText(message: string): Promise<void> {
    return new Promise((resolve, reject) => {
      if (!this.isConnected()) {
        if (this.config.autoReconnect && this.messageQueue.length < this.config.messageBufferSize) {
          const queuedMessage: WSMessage = { type: 'text', data: message };
          this.messageQueue.push(queuedMessage);
          this.emit('messageQueued', queuedMessage);
          resolve();
        } else {
          reject(new Error('WebSocket 未连接且消息队列已满'));
        }
        return;
      }
      
      try {
        this.ws!.send(message);
        this.emit('messageSent', message);
        resolve();
      } catch (error) {
        this.emit('sendError', error);
        reject(error);
      }
    });
  }
  
  /**
   * 发送二进制消息
   */
  public async sendBinary(data: ArrayBuffer | Uint8Array): Promise<void> {
    return new Promise((resolve, reject) => {
      if (!this.isConnected()) {
        if (this.config.autoReconnect && this.messageQueue.length < this.config.messageBufferSize) {
          const queuedMessage: WSMessage = { type: 'binary', data };
          this.messageQueue.push(queuedMessage);
          this.emit('messageQueued', queuedMessage);
          resolve();
        } else {
          reject(new Error('WebSocket 未连接且消息队列已满'));
        }
        return;
      }
      
      try {
        this.ws!.send(data);
        this.emit('messageSent', data);
        resolve();
      } catch (error) {
        this.emit('sendError', error);
        reject(error);
      }
    });
  }
  
  /**
   * 检查连接状态
   */
  public isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }
  
  /**
   * 关闭连接
   */
  public close(code: number = 1000, reason: string = 'Normal closure'): void {
    this.config.autoReconnect = false; // 停止自动重连
    this.stopHeartbeat();
    this.clearReconnectTimer();
    
    if (this.ws) {
      this.ws.close(code, reason);
      this.ws = null;
    }
  }
  
  // 事件监听方法
  public on(event: string, callback: (...args: any[]) => void): this {
    if (!this.callbacks.has(event)) {
      this.callbacks.set(event, []);
    }
    this.callbacks.get(event)!.push(callback);
    return this;
  }
  
  public off(event: string, callback?: (...args: any[]) => void): this {
    const callbacks = this.callbacks.get(event);
    if (!callbacks) return this;
    
    if (callback) {
      const index = callbacks.indexOf(callback);
      if (index > -1) {
        callbacks.splice(index, 1);
      }
    } else {
      this.callbacks.set(event, []);
    }
    return this;
  }
  
  private emit(event: string, ...args: any[]): void {
    const callbacks = this.callbacks.get(event);
    if (callbacks) {
      callbacks.forEach(callback => {
        try {
          callback(...args);
        } catch (error) {
          console.error(`回调函数执行错误 (${event}):`, error);
        }
      });
    }
  }
  
  // 心跳机制
  private startHeartbeat(): void {
    this.stopHeartbeat();
    
    if (this.config.heartbeatInterval > 0) {
      this.heartbeatTimer = window.setInterval(() => {
        if (this.isConnected()) {
          this.sendText('ping').catch(error => {
            console.error('发送心跳失败:', error);
          });
        }
      }, this.config.heartbeatInterval);
    }
  }
  
  private stopHeartbeat(): void {
    if (this.heartbeatTimer) {
      clearInterval(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
  }
  
  // 重连机制
  private scheduleReconnect(): void {
    this.clearReconnectTimer();
    
    const delay = Math.min(
      this.config.reconnectInterval * Math.pow(this.config.reconnectBackoffFactor, this.reconnectAttempts),
      this.config.maxReconnectInterval
    );
    
    console.log(`🔄 将在 ${delay}ms 后尝试重连 (${this.reconnectAttempts + 1}/${this.config.maxReconnectAttempts})`);
    
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectAttempts++;
      this.connect().catch(error => {
        console.error('重连失败:', error);
      });
    }, delay);
  }
  
  private clearReconnectTimer(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }
  
  // 消息队列处理
  private flushMessageQueue(): void {
    while (this.messageQueue.length > 0 && this.isConnected()) {
      const message = this.messageQueue.shift()!;
      
      if (message.type === 'text') {
        this.sendText(typeof message.data === 'string' ? message.data : JSON.stringify(message.data))
          .catch(error => console.error('发送队列消息失败:', error));
      } else if (message.type === 'binary') {
        this.sendBinary(message.data)
          .catch(error => console.error('发送队列消息失败:', error));
      }
    }
  }
  
  // 工具方法
  private generateMessageId(): string {
    return `msg_${++this.messageId}_${Date.now()}`;
  }
  
  // 获取统计信息
  public getStats() {
    return {
      isConnected: this.isConnected(),
      reconnectAttempts: this.reconnectAttempts,
      queuedMessages: this.messageQueue.length,
      config: this.config
    };
  }
}

export { AdvancedWebSocketClient, WSConfig, WSMessage };
```

## ⚛️ React 集成

### useWebSocket Hook

```typescript
// hooks/useWebSocket.ts
import { useEffect, useRef, useState, useCallback } from 'react';
import { AdvancedWebSocketClient, WSMessage, WSConfig } from '../utils/websocket';

interface UseWebSocketOptions extends Partial<WSConfig> {
  onConnected?: () => void;
  onDisconnected?: (error: Error) => void;
  onMessage?: (message: WSMessage) => void;
  onError?: (error: Error) => void;
}

interface UseWebSocketReturn {
  isConnected: boolean;
  isConnecting: boolean;
  error: Error | null;
  sendMessage: (type: string, data: any) => Promise<string | void>;
  sendText: (message: string) => Promise<void>;
  sendJSON: (obj: any) => Promise<string | void>;
  connect: () => Promise<void>;
  disconnect: () => void;
  stats: any;
}

export const useWebSocket = (
  url: string, 
  options: UseWebSocketOptions = {}
): UseWebSocketReturn => {
  const [isConnected, setIsConnected] = useState(false);
  const [isConnecting, setIsConnecting] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [stats, setStats] = useState<any>({});
  
  const clientRef = useRef<AdvancedWebSocketClient | null>(null);
  const { onConnected, onDisconnected, onMessage, onError, ...config } = options;
  
  // 初始化 WebSocket 客户端
  useEffect(() => {
    clientRef.current = new AdvancedWebSocketClient(url, config);
    
    const client = clientRef.current;
    
    // 设置事件监听器
    client
      .on('connected', () => {
        setIsConnected(true);
        setIsConnecting(false);
        setError(null);
        onConnected?.();
      })
      .on('disconnected', (err: Error) => {
        setIsConnected(false);
        setIsConnecting(false);
        onDisconnected?.(err);
      })
      .on('connectError', (err: Error) => {
        setIsConnecting(false);
        setError(err);
        onError?.(err);
      })
      .on('message', (message: WSMessage) => {
        onMessage?.(message);
      })
      .on('reconnecting', (attempts: number) => {
        setIsConnecting(true);
        setError(null);
      });
    
    // 定期更新统计信息
    const statsInterval = setInterval(() => {
      if (client) {
        setStats(client.getStats());
      }
    }, 1000);
    
    return () => {
      clearInterval(statsInterval);
      client.close();
    };
  }, [url]);
  
  const connect = useCallback(async () => {
    if (clientRef.current) {
      setIsConnecting(true);
      try {
        await clientRef.current.connect();
      } catch (err) {
        setError(err as Error);
        setIsConnecting(false);
      }
    }
  }, []);
  
  const disconnect = useCallback(() => {
    if (clientRef.current) {
      clientRef.current.close();
      setIsConnected(false);
      setIsConnecting(false);
    }
  }, []);
  
  const sendMessage = useCallback(async (type: string, data: any) => {
    if (clientRef.current) {
      return await clientRef.current.sendMessage(type, data);
    }
  }, []);
  
  const sendText = useCallback(async (message: string) => {
    if (clientRef.current) {
      return await clientRef.current.sendText(message);
    }
  }, []);
  
  const sendJSON = useCallback(async (obj: any) => {
    if (clientRef.current) {
      return await clientRef.current.sendJSON(obj);
    }
  }, []);
  
  return {
    isConnected,
    isConnecting,
    error,
    sendMessage,
    sendText,
    sendJSON,
    connect,
    disconnect,
    stats
  };
};
```

### React 聊天组件示例

```tsx
// components/ChatRoom.tsx
import React, { useState, useCallback, useEffect } from 'react';
import { useWebSocket } from '../hooks/useWebSocket';

interface ChatMessage {
  id: string;
  user: string;
  text: string;
  timestamp: number;
}

const ChatRoom: React.FC = () => {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [inputText, setInputText] = useState('');
  const [username] = useState(() => `User_${Math.random().toString(36).substr(2, 9)}`);
  
  const { 
    isConnected, 
    isConnecting, 
    error, 
    sendMessage, 
    connect,
    disconnect,
    stats 
  } = useWebSocket('ws://localhost:8080/ws', {
    autoReconnect: true,
    maxReconnectAttempts: 5,
    heartbeatInterval: 30000,
    onConnected: () => {
      console.log('🎉 连接成功!');
      // 发送用户上线消息
      sendMessage('user_join', { username });
    },
    onDisconnected: (err) => {
      console.log('❌ 连接断开:', err.message);
    },
    onMessage: (message) => {
      handleMessage(message);
    },
    onError: (err) => {
      console.error('连接错误:', err);
    }
  });
  
  const handleMessage = useCallback((message: any) => {
    switch (message.type) {
      case 'chat':
        setMessages(prev => [...prev, {
          id: message.id || Date.now().toString(),
          user: message.data.user,
          text: message.data.text,
          timestamp: message.timestamp || Date.now()
        }]);
        break;
      case 'user_join':
        console.log(`👋 ${message.data.username} 加入聊天室`);
        break;
      case 'user_leave':
        console.log(`👋 ${message.data.username} 离开聊天室`);
        break;
      case 'system':
        console.log(`🔔 系统消息: ${message.data.message}`);
        break;
      default:
        console.log('未知消息类型:', message);
    }
  }, []);
  
  const handleSendMessage = async () => {
    if (!inputText.trim() || !isConnected) return;
    
    try {
      await sendMessage('chat', {
        user: username,
        text: inputText.trim()
      });
      setInputText('');
    } catch (error) {
      console.error('发送消息失败:', error);
    }
  };
  
  const handleKeyPress = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSendMessage();
    }
  };
  
  useEffect(() => {
    connect();
  }, [connect]);
  
  return (
    <div className="chat-room">
      <div className="chat-header">
        <h3>💬 WebSocket 聊天室</h3>
        <div className="connection-status">
          <div className={`status-indicator ${isConnected ? 'connected' : 'disconnected'}`}>
            {isConnecting ? '🔄 连接中...' : isConnected ? '🟢 已连接' : '🔴 未连接'}
          </div>
          <button onClick={isConnected ? disconnect : connect}>
            {isConnected ? '断开' : '连接'}
          </button>
        </div>
      </div>
      
      {error && (
        <div className="error-message">
          ❌ 连接错误: {error.message}
        </div>
      )}
      
      <div className="chat-messages">
        {messages.map((msg) => (
          <div key={msg.id} className={`message ${msg.user === username ? 'own' : ''}`}>
            <div className="message-header">
              <span className="username">{msg.user}</span>
              <span className="timestamp">
                {new Date(msg.timestamp).toLocaleTimeString()}
              </span>
            </div>
            <div className="message-text">{msg.text}</div>
          </div>
        ))}
      </div>
      
      <div className="chat-input">
        <textarea
          value={inputText}
          onChange={(e) => setInputText(e.target.value)}
          onKeyPress={handleKeyPress}
          placeholder="输入消息... (Enter 发送)"
          disabled={!isConnected}
          rows={3}
        />
        <button onClick={handleSendMessage} disabled={!isConnected || !inputText.trim()}>
          发送 📤
        </button>
      </div>
      
      <div className="stats">
        <small>
          队列消息: {stats.queuedMessages || 0} | 
          重连次数: {stats.reconnectAttempts || 0}
        </small>
      </div>
    </div>
  );
};

export default ChatRoom;
```

### React 样式文件

```css
/* styles/ChatRoom.css */
.chat-room {
  max-width: 800px;
  margin: 0 auto;
  border: 1px solid #ddd;
  border-radius: 8px;
  overflow: hidden;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
}

.chat-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 1rem;
  background: #f8f9fa;
  border-bottom: 1px solid #ddd;
}

.connection-status {
  display: flex;
  align-items: center;
  gap: 1rem;
}

.status-indicator {
  padding: 0.25rem 0.5rem;
  border-radius: 4px;
  font-size: 0.875rem;
  font-weight: 500;
}

.status-indicator.connected {
  background: #d1edff;
  color: #0969da;
}

.status-indicator.disconnected {
  background: #ffebe9;
  color: #d1242f;
}

.error-message {
  padding: 1rem;
  background: #ffebe9;
  color: #d1242f;
  border-bottom: 1px solid #ddd;
}

.chat-messages {
  height: 400px;
  overflow-y: auto;
  padding: 1rem;
  background: #fff;
}

.message {
  margin-bottom: 1rem;
  padding: 0.75rem;
  border-radius: 8px;
  background: #f8f9fa;
}

.message.own {
  background: #dbeafe;
  margin-left: 2rem;
}

.message-header {
  display: flex;
  justify-content: space-between;
  margin-bottom: 0.5rem;
}

.username {
  font-weight: 600;
  color: #1f2328;
}

.timestamp {
  font-size: 0.75rem;
  color: #656d76;
}

.message-text {
  color: #24292f;
  line-height: 1.4;
}

.chat-input {
  display: flex;
  padding: 1rem;
  background: #f8f9fa;
  border-top: 1px solid #ddd;
  gap: 0.5rem;
}

.chat-input textarea {
  flex: 1;
  padding: 0.5rem;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  resize: none;
  font-family: inherit;
}

.chat-input button {
  padding: 0.5rem 1rem;
  background: #0969da;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-weight: 500;
}

.chat-input button:disabled {
  background: #8c959f;
  cursor: not-allowed;
}

.stats {
  padding: 0.5rem 1rem;
  background: #f8f9fa;
  color: #656d76;
  border-top: 1px solid #ddd;
  text-align: center;
}
```

---

*📖 下一节：[Vue.js 集成](#-vuejs-集成)*
