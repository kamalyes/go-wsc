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
   * 处理接收到的消息 - 支持多种消息格式
   */
  private handleIncomingMessage(event: MessageEvent): void {
    try {
      if (typeof event.data === 'string') {
        this.processStringMessage(event.data);
      } else if (event.data instanceof ArrayBuffer) {
        this.processBinaryMessage(new Uint8Array(event.data));
      } else if (event.data instanceof Blob) {
        this.processBlobMessage(event.data);
      } else {
        console.warn('⚠️ 收到未知类型的消息:', typeof event.data);
      }
    } catch (error) {
      console.error('❌ 处理消息时出错:', error);
      this.emit('messageError', error);
    }
  }
  
  /**
   * 处理字符串消息
   */
  private processStringMessage(data: string): void {
    // 处理心跳响应
    if (data === 'pong') {
      this.emit('pong', data);
      return;
    }
    
    // 处理特殊控制消息
    if (this.isControlMessage(data)) {
      this.handleControlMessage(data);
      return;
    }
    
    // 尝试解析 JSON 消息
    try {
      const message: WSMessage = JSON.parse(data);
      this.processStructuredMessage(message);
    } catch {
      // 普通文本消息
      const textMessage: WSMessage = {
        id: this.generateMessageId(),
        type: 'text',
        data: data,
        timestamp: Date.now()
      };
      this.emit('message', textMessage);
    }
  }
  
  /**
   * 处理结构化消息（JSON）
   */
  private processStructuredMessage(message: WSMessage): void {
    // 验证消息格式
    if (!this.validateMessage(message)) {
      console.warn('⚠️ 收到无效的消息格式:', message);
      return;
    }
    
    // 添加时间戳（如果没有）
    if (!message.timestamp) {
      message.timestamp = Date.now();
    }
    
    // 根据消息类型进行分类处理
    switch (message.type) {
      case 'ack':
        this.handleACKMessage(message);
        break;
      case 'auth':
        this.handleAuthMessage(message);
        break;
      case 'notification':
        this.handleNotificationMessage(message);
        break;
      case 'chat':
        this.handleChatMessage(message);
        break;
      case 'system':
        this.handleSystemMessage(message);
        break;
      case 'error':
        this.handleErrorMessage(message);
        break;
      default:
        // 通用消息处理
        this.emit('message', message);
    }
    
    // 触发类型特定的事件
    this.emit(`message:${message.type}`, message);
  }
  
  /**
   * 处理二进制消息
   */
  private processBinaryMessage(data: Uint8Array): void {
    console.log('📦 收到二进制消息:', data.length, '字节');
    
    // 检查是否是特殊的二进制协议
    if (this.isBinaryProtocolMessage(data)) {
      this.handleBinaryProtocol(data);
    } else {
      // 普通二进制数据
      this.emit('binaryMessage', data);
    }
  }
  
  /**
   * 处理 Blob 消息
   */
  private async processBlobMessage(blob: Blob): Promise<void> {
    try {
      if (blob.type.startsWith('text/')) {
        // 文本类型的 Blob
        const text = await blob.text();
        this.processStringMessage(text);
      } else if (blob.type.startsWith('application/json')) {
        // JSON 类型的 Blob
        const text = await blob.text();
        try {
          const message = JSON.parse(text);
          this.processStructuredMessage(message);
        } catch (error) {
          console.warn('⚠️ 解析 JSON Blob 失败:', error);
        }
      } else {
        // 二进制 Blob
        const buffer = await blob.arrayBuffer();
        this.processBinaryMessage(new Uint8Array(buffer));
      }
    } catch (error) {
      console.error('❌ 处理 Blob 消息失败:', error);
    }
  }
  
  /**
   * 检查是否是控制消息
   */
  private isControlMessage(data: string): boolean {
    const controlCommands = ['ping', 'pong', 'close', 'heartbeat'];
    return controlCommands.includes(data.toLowerCase());
  }
  
  /**
   * 处理控制消息
   */
  private handleControlMessage(command: string): void {
    switch (command.toLowerCase()) {
      case 'ping':
        this.emit('ping', command);
        // 自动回复 pong
        this.sendText('pong').catch(console.error);
        break;
      case 'heartbeat':
        this.emit('heartbeat', command);
        break;
      case 'close':
        this.emit('closeRequest', command);
        break;
    }
  }
  
  /**
   * 验证消息格式
   */
  private validateMessage(message: any): message is WSMessage {
    return message && 
           typeof message === 'object' && 
           typeof message.type === 'string' &&
           message.data !== undefined;
  }
  
  /**
   * 处理 ACK 消息
   */
  private handleACKMessage(message: WSMessage): void {
    this.emit('ackReceived', message);
    
    // 如果这是对我们发送消息的确认
    if (message.data && typeof message.data === 'object') {
      const ackData = message.data as any;
      if (ackData.messageId) {
        this.emit('messageConfirmed', ackData.messageId, message);
      }
    }
  }
  
  /**
   * 处理认证消息
   */
  private handleAuthMessage(message: WSMessage): void {
    this.emit('authResponse', message);
    
    if (message.data && typeof message.data === 'object') {
      const authData = message.data as any;
      if (authData.status === 'success') {
        this.emit('authenticated', authData);
      } else {
        this.emit('authFailed', authData);
      }
    }
  }
  
  /**
   * 处理通知消息
   */
  private handleNotificationMessage(message: WSMessage): void {
    this.emit('notification', message);
    
    // 根据通知级别分类
    if (message.data && typeof message.data === 'object') {
      const notificationData = message.data as any;
      const level = notificationData.level || 'info';
      this.emit(`notification:${level}`, message);
    }
  }
  
  /**
   * 处理聊天消息
   */
  private handleChatMessage(message: WSMessage): void {
    this.emit('chatMessage', message);
    
    // 根据聊天类型分类
    if (message.data && typeof message.data === 'object') {
      const chatData = message.data as any;
      if (chatData.room) {
        this.emit(`chat:${chatData.room}`, message);
      }
      if (chatData.from) {
        this.emit(`chat:from:${chatData.from}`, message);
      }
    }
  }
  
  /**
   * 处理系统消息
   */
  private handleSystemMessage(message: WSMessage): void {
    this.emit('systemMessage', message);
    
    if (message.data && typeof message.data === 'object') {
      const systemData = message.data as any;
      
      // 处理系统命令
      if (systemData.command) {
        this.handleSystemCommand(systemData.command, systemData);
      }
    }
  }
  
  /**
   * 处理错误消息
   */
  private handleErrorMessage(message: WSMessage): void {
    this.emit('serverError', message);
    console.error('🚨 服务器错误:', message.data);
  }
  
  /**
   * 处理系统命令
   */
  private handleSystemCommand(command: string, data: any): void {
    switch (command) {
      case 'reload':
        this.emit('systemReload', data);
        break;
      case 'maintenance':
        this.emit('systemMaintenance', data);
        break;
      case 'update':
        this.emit('systemUpdate', data);
        break;
      default:
        this.emit('systemCommand', command, data);
    }
  }
  
  /**
   * 检查是否是二进制协议消息
   */
  private isBinaryProtocolMessage(data: Uint8Array): boolean {
    // 检查魔术字节或协议头
    return data.length > 4 && 
           data[0] === 0x01 && 
           data[1] === 0x02; // 示例协议标识
  }
  
  /**
   * 处理二进制协议
   */
  private handleBinaryProtocol(data: Uint8Array): void {
    // 解析二进制协议
    const header = data.slice(0, 8);
    const payload = data.slice(8);
    
    this.emit('binaryProtocol', { header, payload });
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

## 🌐 Vue.js 集成

### Vue 3 组合式 API

```typescript
// composables/useWebSocket.ts
import { ref, reactive, onMounted, onUnmounted, computed, watch } from 'vue';
import { AdvancedWebSocketClient, WSMessage, WSConfig } from './websocket-client';

export interface UseWebSocketOptions extends Partial<WSConfig> {
  immediate?: boolean;
  onMessage?: (message: WSMessage) => void;
  onError?: (error: Error) => void;
  onConnect?: () => void;
  onDisconnect?: (error?: Error) => void;
}

export interface WebSocketComposable {
  // 状态
  isConnected: Ref<boolean>;
  isConnecting: Ref<boolean>;
  error: Ref<Error | null>;
  stats: Ref<any>;
  messages: Ref<WSMessage[]>;
  
  // 方法
  connect: () => Promise<void>;
  disconnect: () => void;
  sendMessage: (type: string, data: any) => Promise<string | void>;
  sendText: (message: string) => Promise<void>;
  sendJSON: (obj: any) => Promise<string | void>;
  clearMessages: () => void;
  
  // 事件订阅
  on: (event: string, handler: Function) => void;
  off: (event: string, handler?: Function) => void;
}

/**
 * Vue 3 WebSocket 组合式函数
 */
export function useWebSocket(
  url: string, 
  options: UseWebSocketOptions = {}
): WebSocketComposable {
  const {
    immediate = true,
    onMessage,
    onError,
    onConnect,
    onDisconnect,
    ...wsConfig
  } = options;
  
  // 响应式状态
  const isConnected = ref(false);
  const isConnecting = ref(false);
  const error = ref<Error | null>(null);
  const stats = ref<any>({});
  const messages = ref<WSMessage[]>([]);
  
  // WebSocket 客户端实例
  let client: AdvancedWebSocketClient | null = null;
  const eventHandlers = new Map<string, Function[]>();
  
  // 初始化 WebSocket 客户端
  const initClient = () => {
    if (client) {
      client.close();
    }
    
    client = new AdvancedWebSocketClient(url, {
      autoReconnect: true,
      maxReconnectAttempts: 5,
      heartbeatInterval: 30000,
      ...wsConfig
    });
    
    setupEventHandlers();
  };
  
  // 设置事件处理器
  const setupEventHandlers = () => {
    if (!client) return;
    
    client
      .on('connected', () => {
        isConnected.value = true;
        isConnecting.value = false;
        error.value = null;
        console.log('✅ Vue WebSocket 连接成功');
        onConnect?.();
      })
      .on('disconnected', (err: Error) => {
        isConnected.value = false;
        isConnecting.value = false;
        error.value = err;
        console.warn('⚠️ Vue WebSocket 连接断开:', err.message);
        onDisconnect?.(err);
      })
      .on('connectError', (err: Error) => {
        isConnected.value = false;
        isConnecting.value = false;
        error.value = err;
        console.error('❌ Vue WebSocket 连接失败:', err.message);
        onError?.(err);
      })
      .on('reconnecting', (attempts: number) => {
        isConnecting.value = true;
        console.log(`🔄 Vue WebSocket 重连中... (${attempts})`);
      })
      .on('message', (message: WSMessage) => {
        // 添加到消息列表
        messages.value.push(message);
        
        // 限制消息数量（避免内存泄漏）
        if (messages.value.length > 1000) {
          messages.value = messages.value.slice(-500);
        }
        
        // 调用用户回调
        onMessage?.(message);
        
        // 触发自定义事件处理器
        triggerEventHandlers('message', message);
      })
      .on('binaryMessage', (data: Uint8Array) => {
        const message: WSMessage = {
          id: `binary-${Date.now()}`,
          type: 'binary',
          data: data,
          timestamp: Date.now()
        };
        messages.value.push(message);
        triggerEventHandlers('binaryMessage', data);
      })
      .on('messageSent', (data: any) => {
        console.log('📤 Vue WebSocket 消息已发送:', data);
        triggerEventHandlers('messageSent', data);
      })
      .on('sendError', (err: Error) => {
        console.error('❌ Vue WebSocket 发送失败:', err.message);
        triggerEventHandlers('sendError', err);
      });
  };
  
  // 触发自定义事件处理器
  const triggerEventHandlers = (event: string, ...args: any[]) => {
    const handlers = eventHandlers.get(event);
    if (handlers) {
      handlers.forEach(handler => {
        try {
          handler(...args);
        } catch (err) {
          console.error(`事件处理器 ${event} 执行错误:`, err);
        }
      });
    }
  };
  
  // 连接方法
  const connect = async (): Promise<void> => {
    if (!client) {
      initClient();
    }
    
    if (client && !isConnected.value && !isConnecting.value) {
      isConnecting.value = true;
      error.value = null;
      try {
        await client.connect();
      } catch (err) {
        isConnecting.value = false;
        error.value = err as Error;
        throw err;
      }
    }
  };
  
  // 断开连接
  const disconnect = (): void => {
    if (client) {
      client.close();
      client = null;
    }
    isConnected.value = false;
    isConnecting.value = false;
  };
  
  // 发送消息方法
  const sendMessage = async (type: string, data: any): Promise<string | void> => {
    if (!client || !isConnected.value) {
      throw new Error('WebSocket 未连接');
    }
    return await client.sendMessage(type, data);
  };
  
  const sendText = async (message: string): Promise<void> => {
    if (!client || !isConnected.value) {
      throw new Error('WebSocket 未连接');
    }
    await client.sendText(message);
  };
  
  const sendJSON = async (obj: any): Promise<string | void> => {
    if (!client || !isConnected.value) {
      throw new Error('WebSocket 未连接');
    }
    return await client.sendJSON(obj);
  };
  
  // 清空消息
  const clearMessages = (): void => {
    messages.value = [];
  };
  
  // 事件订阅方法
  const on = (event: string, handler: Function): void => {
    if (!eventHandlers.has(event)) {
      eventHandlers.set(event, []);
    }
    eventHandlers.get(event)!.push(handler);
  };
  
  const off = (event: string, handler?: Function): void => {
    const handlers = eventHandlers.get(event);
    if (!handlers) return;
    
    if (handler) {
      const index = handlers.indexOf(handler);
      if (index > -1) {
        handlers.splice(index, 1);
      }
    } else {
      eventHandlers.set(event, []);
    }
  };
  
  // 监听 URL 变化，重新连接
  watch(() => url, (newUrl, oldUrl) => {
    if (newUrl !== oldUrl && isConnected.value) {
      disconnect();
      // 延迟重连，避免频繁连接
      setTimeout(() => {
        if (immediate) {
          connect().catch(console.error);
        }
      }, 1000);
    }
  });
  
  // 生命周期钩子
  onMounted(() => {
    if (immediate) {
      initClient();
      connect().catch(console.error);
    }
  });
  
  onUnmounted(() => {
    disconnect();
    eventHandlers.clear();
  });
  
  // 计算属性
  const connectionStatus = computed(() => {
    if (isConnecting.value) return 'connecting';
    if (isConnected.value) return 'connected';
    if (error.value) return 'error';
    return 'disconnected';
  });
  
  return {
    isConnected,
    isConnecting,
    error,
    stats,
    messages,
    connect,
    disconnect,
    sendMessage,
    sendText,
    sendJSON,
    clearMessages,
    on,
    off,
    
    // 额外的计算属性
    connectionStatus
  };
}
```

### Vue 3 聊天室组件示例

```vue
<template>
  <div class="chat-room">
    <!-- 连接状态栏 -->
    <div class="status-bar" :class="connectionStatusClass">
      <div class="status-indicator">
        <span class="indicator-dot" :class="connectionStatusClass"></span>
        <span class="status-text">{{ connectionStatusText }}</span>
      </div>
      <div class="stats">
        <span>消息: {{ messages.length }}</span>
        <span v-if="isConnected">在线用户: {{ onlineUsers.length }}</span>
      </div>
    </div>
    
    <!-- 聊天消息区域 -->
    <div class="chat-messages" ref="messagesContainer">
      <div 
        v-for="message in chatMessages" 
        :key="message.id"
        class="message"
        :class="{ 'own-message': message.isOwn }"
      >
        <div class="message-header">
          <span class="username">{{ message.username }}</span>
          <span class="timestamp">{{ formatTime(message.timestamp) }}</span>
        </div>
        <div class="message-content">
          <template v-if="message.type === 'text'">
            {{ message.content }}
          </template>
          <template v-else-if="message.type === 'image'">
            <img :src="message.data.url" :alt="message.data.alt" class="message-image">
          </template>
          <template v-else-if="message.type === 'file'">
            <div class="file-message">
              <i class="file-icon">📄</i>
              <a :href="message.data.url" :download="message.data.name">
                {{ message.data.name }}
              </a>
            </div>
          </template>
          <template v-else>
            <div class="system-message">{{ message.content }}</div>
          </template>
        </div>
      </div>
    </div>
    
    <!-- 输入区域 -->
    <div class="chat-input" v-if="isConnected">
      <div class="input-tools">
        <button @click="showEmojiPicker = !showEmojiPicker" class="tool-button">
          😀
        </button>
        <input 
          type="file" 
          ref="fileInput" 
          @change="handleFileUpload" 
          accept="image/*,.pdf,.doc,.docx"
          style="display: none"
        >
        <button @click="$refs.fileInput.click()" class="tool-button">
          📎
        </button>
      </div>
      
      <div class="message-input-wrapper">
        <textarea
          v-model="inputText"
          @keydown="handleKeyDown"
          @input="handleInput"
          placeholder="输入消息... (Shift+Enter 换行，Enter 发送)"
          class="message-input"
          :disabled="!isConnected"
          rows="1"
          ref="textInput"
        ></textarea>
        
        <button 
          @click="sendMessage"
          :disabled="!canSend"
          class="send-button"
          :class="{ 'can-send': canSend }"
        >
          <span v-if="isTyping">⏳</span>
          <span v-else>📤</span>
        </button>
      </div>
    </div>
    
    <!-- 表情选择器 -->
    <div v-if="showEmojiPicker" class="emoji-picker">
      <div class="emoji-grid">
        <button 
          v-for="emoji in commonEmojis" 
          :key="emoji"
          @click="insertEmoji(emoji)"
          class="emoji-button"
        >
          {{ emoji }}
        </button>
      </div>
    </div>
    
    <!-- 重连按钮 -->
    <div v-if="error && !isConnecting" class="reconnect-bar">
      <span class="error-message">连接失败: {{ error.message }}</span>
      <button @click="reconnect" class="reconnect-button">
        重新连接
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, nextTick, onMounted, watch } from 'vue';
import { useWebSocket, WSMessage } from '../composables/useWebSocket';

// 属性定义
interface Props {
  serverUrl?: string;
  username?: string;
  roomId?: string;
}

const props = withDefaults(defineProps<Props>(), {
  serverUrl: 'ws://localhost:8080/ws',
  username: () => `User_${Math.random().toString(36).substr(2, 6)}`,
  roomId: 'general'
});

// 响应式数据
const inputText = ref('');
const isTyping = ref(false);
const showEmojiPicker = ref(false);
const onlineUsers = ref<string[]>([]);
const messagesContainer = ref<HTMLElement>();
const textInput = ref<HTMLTextAreaElement>();
const fileInput = ref<HTMLInputElement>();

// WebSocket 连接
const {
  isConnected,
  isConnecting,
  error,
  messages,
  connect,
  disconnect,
  sendMessage: wsSendMessage,
  sendText,
  on,
  off
} = useWebSocket(props.serverUrl, {
  immediate: true,
  onConnect: () => {
    console.log('✅ 聊天室连接成功');
    // 发送加入房间消息
    joinRoom();
  },
  onDisconnect: (err) => {
    console.warn('⚠️ 聊天室连接断开:', err?.message);
    onlineUsers.value = [];
  },
  onMessage: handleIncomingMessage
});

// 常用表情
const commonEmojis = [
  '😀', '😂', '🤔', '👍', '❤️', '🎉', '🔥', '💯',
  '😊', '😎', '🤗', '👋', '🙏', '✨', '🚀', '💪'
];

// 计算属性
const connectionStatusClass = computed(() => {
  if (isConnecting.value) return 'connecting';
  if (isConnected.value) return 'connected';
  if (error.value) return 'error';
  return 'disconnected';
});

const connectionStatusText = computed(() => {
  if (isConnecting.value) return '连接中...';
  if (isConnected.value) return '已连接';
  if (error.value) return '连接失败';
  return '未连接';
});

const chatMessages = computed(() => {
  return messages.value
    .filter(msg => msg.type === 'chat' || msg.type === 'system')
    .map(msg => ({
      ...msg,
      isOwn: msg.data?.username === props.username,
      username: msg.data?.username || '系统',
      content: msg.data?.content || msg.data?.message || '',
      type: msg.data?.messageType || 'text'
    }));
});

const canSend = computed(() => {
  return isConnected.value && inputText.value.trim().length > 0 && !isTyping.value;
});

// 方法
const handleIncomingMessage = (message: WSMessage) => {
  console.log('📨 收到消息:', message);
  
  switch (message.type) {
    case 'userList':
      onlineUsers.value = message.data?.users || [];
      break;
    case 'userJoined':
      if (!onlineUsers.value.includes(message.data?.username)) {
        onlineUsers.value.push(message.data?.username);
      }
      break;
    case 'userLeft':
      onlineUsers.value = onlineUsers.value.filter(
        user => user !== message.data?.username
      );
      break;
    case 'typing':
      // 处理输入状态
      handleTypingIndicator(message.data);
      break;
  }
  
  // 自动滚动到底部
  nextTick(() => {
    scrollToBottom();
  });
};

const joinRoom = async () => {
  try {
    await wsSendMessage('join', {
      username: props.username,
      roomId: props.roomId,
      timestamp: Date.now()
    });
    console.log(`🚪 加入房间: ${props.roomId}`);
  } catch (err) {
    console.error('❌ 加入房间失败:', err);
  }
};

const sendMessage = async () => {
  if (!canSend.value) return;
  
  const message = inputText.value.trim();
  if (!message) return;
  
  isTyping.value = true;
  
  try {
    await wsSendMessage('chat', {
      username: props.username,
      content: message,
      roomId: props.roomId,
      messageType: 'text',
      timestamp: Date.now()
    });
    
    inputText.value = '';
    adjustTextareaHeight();
  } catch (err) {
    console.error('❌ 发送消息失败:', err);
    // 可以在这里显示错误提示
  } finally {
    isTyping.value = false;
  }
};

const handleKeyDown = (event: KeyboardEvent) => {
  if (event.key === 'Enter' && !event.shiftKey) {
    event.preventDefault();
    sendMessage();
  }
};

const handleInput = () => {
  adjustTextareaHeight();
  
  // 发送输入状态
  sendTypingIndicator();
};

const adjustTextareaHeight = () => {
  if (textInput.value) {
    textInput.value.style.height = 'auto';
    textInput.value.style.height = Math.min(textInput.value.scrollHeight, 120) + 'px';
  }
};

const sendTypingIndicator = debounce(() => {
  if (isConnected.value && inputText.value.trim()) {
    wsSendMessage('typing', {
      username: props.username,
      roomId: props.roomId,
      isTyping: true
    }).catch(console.error);
  }
}, 300);

const handleTypingIndicator = (data: any) => {
  // 实现输入指示器逻辑
  console.log(`${data.username} 正在输入...`);
};

const insertEmoji = (emoji: string) => {
  inputText.value += emoji;
  showEmojiPicker.value = false;
  textInput.value?.focus();
};

const handleFileUpload = async (event: Event) => {
  const target = event.target as HTMLInputElement;
  const file = target.files?.[0];
  
  if (!file) return;
  
  try {
    // 这里需要实现文件上传逻辑
    const fileUrl = await uploadFile(file);
    
    await wsSendMessage('chat', {
      username: props.username,
      content: `发送了文件: ${file.name}`,
      roomId: props.roomId,
      messageType: file.type.startsWith('image/') ? 'image' : 'file',
      data: {
        name: file.name,
        url: fileUrl,
        size: file.size,
        type: file.type
      },
      timestamp: Date.now()
    });
    
  } catch (err) {
    console.error('❌ 文件上传失败:', err);
  }
  
  // 清空文件输入
  target.value = '';
};

const uploadFile = async (file: File): Promise<string> => {
  // 实现文件上传逻辑
  // 这里返回一个示例 URL
  return `https://example.com/files/${file.name}`;
};

const scrollToBottom = () => {
  if (messagesContainer.value) {
    messagesContainer.value.scrollTop = messagesContainer.value.scrollHeight;
  }
};

const formatTime = (timestamp: number): string => {
  return new Date(timestamp).toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit'
  });
};

const reconnect = () => {
  connect().catch(console.error);
};

// 防抖函数
function debounce(func: Function, wait: number) {
  let timeout: NodeJS.Timeout;
  return function(this: any, ...args: any[]) {
    clearTimeout(timeout);
    timeout = setTimeout(() => func.apply(this, args), wait);
  };
}

// 生命周期
onMounted(() => {
  // 设置事件监听器
  on('message', (message: WSMessage) => {
    console.log('组件内收到消息:', message);
  });
  
  // 焦点到输入框
  nextTick(() => {
    textInput.value?.focus();
  });
});

// 监听消息变化，自动滚动
watch(() => messages.value.length, () => {
  nextTick(() => {
    scrollToBottom();
  });
});

// 点击外部关闭表情选择器
const handleClickOutside = (event: Event) => {
  const target = event.target as HTMLElement;
  if (!target.closest('.emoji-picker') && !target.closest('.tool-button')) {
    showEmojiPicker.value = false;
  }
};

onMounted(() => {
  document.addEventListener('click', handleClickOutside);
});

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside);
});
</script>

<style scoped>
.chat-room {
  display: flex;
  flex-direction: column;
  height: 600px;
  border: 1px solid #ddd;
  border-radius: 8px;
  overflow: hidden;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
}

.status-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 8px 16px;
  background: #f5f5f5;
  border-bottom: 1px solid #ddd;
  font-size: 14px;
}

.status-indicator {
  display: flex;
  align-items: center;
  gap: 8px;
}

.indicator-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: block;
}

.indicator-dot.connected { background: #4caf50; }
.indicator-dot.connecting { 
  background: #ff9800; 
  animation: pulse 1.5s infinite;
}
.indicator-dot.error { background: #f44336; }
.indicator-dot.disconnected { background: #9e9e9e; }

.stats {
  display: flex;
  gap: 16px;
  font-size: 12px;
  color: #666;
}

.chat-messages {
  flex: 1;
  padding: 16px;
  overflow-y: auto;
  background: #fff;
}

.message {
  margin-bottom: 16px;
  max-width: 80%;
}

.message.own-message {
  margin-left: auto;
  text-align: right;
}

.message-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
  font-size: 12px;
  color: #666;
}

.message.own-message .message-header {
  justify-content: flex-end;
}

.username {
  font-weight: 600;
  color: #2196f3;
}

.message-content {
  padding: 8px 12px;
  border-radius: 18px;
  background: #f0f0f0;
  word-wrap: break-word;
}

.own-message .message-content {
  background: #2196f3;
  color: white;
}

.message-image {
  max-width: 200px;
  border-radius: 8px;
}

.file-message {
  display: flex;
  align-items: center;
  gap: 8px;
}

.file-icon {
  font-size: 16px;
}

.system-message {
  font-style: italic;
  color: #666;
}

.chat-input {
  padding: 16px;
  border-top: 1px solid #ddd;
  background: #fafafa;
}

.input-tools {
  display: flex;
  gap: 8px;
  margin-bottom: 8px;
}

.tool-button {
  padding: 6px 8px;
  border: 1px solid #ddd;
  border-radius: 4px;
  background: white;
  cursor: pointer;
  font-size: 14px;
}

.tool-button:hover {
  background: #f5f5f5;
}

.message-input-wrapper {
  display: flex;
  gap: 8px;
  align-items: flex-end;
}

.message-input {
  flex: 1;
  min-height: 20px;
  max-height: 120px;
  padding: 8px 12px;
  border: 1px solid #ddd;
  border-radius: 20px;
  font-size: 14px;
  font-family: inherit;
  resize: none;
  overflow-y: auto;
}

.message-input:focus {
  outline: none;
  border-color: #2196f3;
}

.send-button {
  padding: 8px 12px;
  border: none;
  border-radius: 50%;
  background: #ddd;
  cursor: pointer;
  font-size: 16px;
  transition: all 0.2s;
}

.send-button.can-send {
  background: #2196f3;
  color: white;
}

.send-button:hover.can-send {
  background: #1976d2;
  transform: scale(1.05);
}

.emoji-picker {
  position: absolute;
  bottom: 80px;
  left: 16px;
  background: white;
  border: 1px solid #ddd;
  border-radius: 8px;
  padding: 8px;
  box-shadow: 0 4px 12px rgba(0,0,0,0.1);
  z-index: 1000;
}

.emoji-grid {
  display: grid;
  grid-template-columns: repeat(8, 1fr);
  gap: 4px;
}

.emoji-button {
  padding: 4px;
  border: none;
  background: none;
  cursor: pointer;
  font-size: 18px;
  border-radius: 4px;
}

.emoji-button:hover {
  background: #f5f5f5;
}

.reconnect-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 8px 16px;
  background: #fff3cd;
  border-top: 1px solid #ffeaa7;
  color: #856404;
  font-size: 14px;
}

.reconnect-button {
  padding: 4px 12px;
  border: 1px solid #ffc107;
  border-radius: 4px;
  background: #ffc107;
  color: #212529;
  cursor: pointer;
  font-size: 12px;
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.5; }
}
</style>
```

### Vue 2 Options API 版本

```vue
<template>
  <!-- 与 Vue 3 版本基本相同的模板 -->
</template>

<script lang="ts">
import { defineComponent, PropType } from 'vue';
import { AdvancedWebSocketClient, WSMessage } from '../websocket-client';

export default defineComponent({
  name: 'ChatRoom',
  
  props: {
    serverUrl: {
      type: String,
      default: 'ws://localhost:8080/ws'
    },
    username: {
      type: String,
      default: () => `User_${Math.random().toString(36).substr(2, 6)}`
    },
    roomId: {
      type: String,
      default: 'general'
    }
  },
  
  data() {
    return {
      client: null as AdvancedWebSocketClient | null,
      isConnected: false,
      isConnecting: false,
      error: null as Error | null,
      messages: [] as WSMessage[],
      inputText: '',
      isTyping: false,
      showEmojiPicker: false,
      onlineUsers: [] as string[],
      
      commonEmojis: [
        '😀', '😂', '🤔', '👍', '❤️', '🎉', '🔥', '💯',
        '😊', '😎', '🤗', '👋', '🙏', '✨', '🚀', '💪'
      ]
    };
  },
  
  computed: {
    connectionStatusClass(): string {
      if (this.isConnecting) return 'connecting';
      if (this.isConnected) return 'connected';
      if (this.error) return 'error';
      return 'disconnected';
    },
    
    connectionStatusText(): string {
      if (this.isConnecting) return '连接中...';
      if (this.isConnected) return '已连接';
      if (this.error) return '连接失败';
      return '未连接';
    },
    
    chatMessages(): any[] {
      return this.messages
        .filter(msg => msg.type === 'chat' || msg.type === 'system')
        .map(msg => ({
          ...msg,
          isOwn: msg.data?.username === this.username,
          username: msg.data?.username || '系统',
          content: msg.data?.content || msg.data?.message || '',
          messageType: msg.data?.messageType || 'text'
        }));
    },
    
    canSend(): boolean {
      return this.isConnected && 
             this.inputText.trim().length > 0 && 
             !this.isTyping;
    }
  },
  
  watch: {
    serverUrl: {
      handler(newUrl, oldUrl) {
        if (newUrl !== oldUrl && this.isConnected) {
          this.disconnect();
          this.$nextTick(() => {
            this.initClient();
            this.connect();
          });
        }
      }
    },
    
    'messages.length': {
      handler() {
        this.$nextTick(() => {
          this.scrollToBottom();
        });
      }
    }
  },
  
  mounted() {
    this.initClient();
    this.connect();
    
    // 添加点击外部事件监听
    document.addEventListener('click', this.handleClickOutside);
  },
  
  beforeDestroy() {
    this.disconnect();
    document.removeEventListener('click', this.handleClickOutside);
  },
  
  methods: {
    initClient() {
      if (this.client) {
        this.client.close();
      }
      
      this.client = new AdvancedWebSocketClient(this.serverUrl, {
        autoReconnect: true,
        maxReconnectAttempts: 5,
        heartbeatInterval: 30000
      });
      
      this.setupEventHandlers();
    },
    
    setupEventHandlers() {
      if (!this.client) return;
      
      this.client
        .on('connected', () => {
          this.isConnected = true;
          this.isConnecting = false;
          this.error = null;
          console.log('✅ Vue 2 WebSocket 连接成功');
          this.joinRoom();
        })
        .on('disconnected', (err: Error) => {
          this.isConnected = false;
          this.isConnecting = false;
          this.error = err;
          this.onlineUsers = [];
          console.warn('⚠️ Vue 2 WebSocket 连接断开:', err.message);
        })
        .on('connectError', (err: Error) => {
          this.isConnected = false;
          this.isConnecting = false;
          this.error = err;
          console.error('❌ Vue 2 WebSocket 连接失败:', err.message);
        })
        .on('reconnecting', (attempts: number) => {
          this.isConnecting = true;
          console.log(`🔄 Vue 2 WebSocket 重连中... (${attempts})`);
        })
        .on('message', this.handleIncomingMessage);
    },
    
    async connect() {
      if (!this.client) return;
      
      this.isConnecting = true;
      this.error = null;
      
      try {
        await this.client.connect();
      } catch (err) {
        this.isConnecting = false;
        this.error = err as Error;
        console.error('连接失败:', err);
      }
    },
    
    disconnect() {
      if (this.client) {
        this.client.close();
        this.client = null;
      }
      this.isConnected = false;
      this.isConnecting = false;
    },
    
    handleIncomingMessage(message: WSMessage) {
      this.messages.push(message);
      
      // 限制消息数量
      if (this.messages.length > 1000) {
        this.messages = this.messages.slice(-500);
      }
      
      // 处理特殊消息类型
      switch (message.type) {
        case 'userList':
          this.onlineUsers = message.data?.users || [];
          break;
        case 'userJoined':
          if (!this.onlineUsers.includes(message.data?.username)) {
            this.onlineUsers.push(message.data?.username);
          }
          break;
        case 'userLeft':
          this.onlineUsers = this.onlineUsers.filter(
            user => user !== message.data?.username
          );
          break;
      }
    },
    
    async joinRoom() {
      if (!this.client) return;
      
      try {
        await this.client.sendMessage('join', {
          username: this.username,
          roomId: this.roomId,
          timestamp: Date.now()
        });
        console.log(`🚪 加入房间: ${this.roomId}`);
      } catch (err) {
        console.error('❌ 加入房间失败:', err);
      }
    },
    
    async sendMessage() {
      if (!this.canSend || !this.client) return;
      
      const message = this.inputText.trim();
      if (!message) return;
      
      this.isTyping = true;
      
      try {
        await this.client.sendMessage('chat', {
          username: this.username,
          content: message,
          roomId: this.roomId,
          messageType: 'text',
          timestamp: Date.now()
        });
        
        this.inputText = '';
        this.adjustTextareaHeight();
      } catch (err) {
        console.error('❌ 发送消息失败:', err);
      } finally {
        this.isTyping = false;
      }
    },
    
    handleKeyDown(event: KeyboardEvent) {
      if (event.key === 'Enter' && !event.shiftKey) {
        event.preventDefault();
        this.sendMessage();
      }
    },
    
    handleInput() {
      this.adjustTextareaHeight();
    },
    
    adjustTextareaHeight() {
      const textarea = this.$refs.textInput as HTMLTextAreaElement;
      if (textarea) {
        textarea.style.height = 'auto';
        textarea.style.height = Math.min(textarea.scrollHeight, 120) + 'px';
      }
    },
    
    insertEmoji(emoji: string) {
      this.inputText += emoji;
      this.showEmojiPicker = false;
      (this.$refs.textInput as HTMLTextAreaElement)?.focus();
    },
    
    scrollToBottom() {
      const container = this.$refs.messagesContainer as HTMLElement;
      if (container) {
        container.scrollTop = container.scrollHeight;
      }
    },
    
    formatTime(timestamp: number): string {
      return new Date(timestamp).toLocaleTimeString('zh-CN', {
        hour: '2-digit',
        minute: '2-digit'
      });
    },
    
    handleClickOutside(event: Event) {
      const target = event.target as HTMLElement;
      if (!target.closest('.emoji-picker') && !target.closest('.tool-button')) {
        this.showEmojiPicker = false;
      }
    },
    
    reconnect() {
      this.connect();
    }
  }
});
</script>

<style scoped>
/* 与 Vue 3 版本相同的样式 */
</style>
```

### Pinia 状态管理集成

```typescript
// stores/websocket.ts
import { defineStore } from 'pinia';
import { AdvancedWebSocketClient, WSMessage } from '../websocket-client';

export interface WebSocketState {
  client: AdvancedWebSocketClient | null;
  isConnected: boolean;
  isConnecting: boolean;
  error: Error | null;
  messages: WSMessage[];
  onlineUsers: string[];
  currentRoom: string;
  stats: {
    totalMessages: number;
    lastMessageTime: number;
  };
}

export const useWebSocketStore = defineStore('websocket', {
  state: (): WebSocketState => ({
    client: null,
    isConnected: false,
    isConnecting: false,
    error: null,
    messages: [],
    onlineUsers: [],
    currentRoom: 'general',
    stats: {
      totalMessages: 0,
      lastMessageTime: 0
    }
  }),
  
  getters: {
    connectionStatus: (state) => {
      if (state.isConnecting) return 'connecting';
      if (state.isConnected) return 'connected';
      if (state.error) return 'error';
      return 'disconnected';
    },
    
    roomMessages: (state) => {
      return state.messages.filter(msg => 
        msg.data?.roomId === state.currentRoom || 
        msg.type === 'system'
      );
    },
    
    unreadCount: (state) => {
      // 计算未读消息数量的逻辑
      return state.messages.filter(msg => !msg.read).length;
    }
  },
  
  actions: {
    async initClient(url: string, options = {}) {
      if (this.client) {
        this.client.close();
      }
      
      this.client = new AdvancedWebSocketClient(url, {
        autoReconnect: true,
        maxReconnectAttempts: 10,
        heartbeatInterval: 30000,
        ...options
      });
      
      this.setupEventHandlers();
    },
    
    setupEventHandlers() {
      if (!this.client) return;
      
      this.client
        .on('connected', () => {
          this.isConnected = true;
          this.isConnecting = false;
          this.error = null;
          console.log('✅ Pinia WebSocket 连接成功');
        })
        .on('disconnected', (err: Error) => {
          this.isConnected = false;
          this.isConnecting = false;
          this.error = err;
          this.onlineUsers = [];
          console.warn('⚠️ Pinia WebSocket 断开:', err.message);
        })
        .on('connectError', (err: Error) => {
          this.isConnected = false;
          this.isConnecting = false;
          this.error = err;
          console.error('❌ Pinia WebSocket 连接失败:', err.message);
        })
        .on('message', this.handleMessage);
    },
    
    async connect() {
      if (!this.client) return;
      
      this.isConnecting = true;
      this.error = null;
      
      try {
        await this.client.connect();
      } catch (err) {
        this.isConnecting = false;
        this.error = err as Error;
        throw err;
      }
    },
    
    disconnect() {
      if (this.client) {
        this.client.close();
        this.client = null;
      }
      this.isConnected = false;
      this.isConnecting = false;
      this.onlineUsers = [];
    },
    
    handleMessage(message: WSMessage) {
      this.messages.push(message);
      this.stats.totalMessages++;
      this.stats.lastMessageTime = Date.now();
      
      // 限制消息数量
      if (this.messages.length > 2000) {
        this.messages = this.messages.slice(-1000);
      }
      
      // 处理特殊消息
      switch (message.type) {
        case 'userList':
          this.onlineUsers = message.data?.users || [];
          break;
        case 'userJoined':
          this.addOnlineUser(message.data?.username);
          break;
        case 'userLeft':
          this.removeOnlineUser(message.data?.username);
          break;
      }
    },
    
    async sendMessage(type: string, data: any) {
      if (!this.client || !this.isConnected) {
        throw new Error('WebSocket 未连接');
      }
      
      return await this.client.sendMessage(type, data);
    },
    
    async sendChat(content: string, username: string) {
      return await this.sendMessage('chat', {
        username,
        content,
        roomId: this.currentRoom,
        messageType: 'text',
        timestamp: Date.now()
      });
    },
    
    async joinRoom(roomId: string, username: string) {
      this.currentRoom = roomId;
      
      return await this.sendMessage('join', {
        username,
        roomId,
        timestamp: Date.now()
      });
    },
    
    async leaveRoom(username: string) {
      return await this.sendMessage('leave', {
        username,
        roomId: this.currentRoom,
        timestamp: Date.now()
      });
    },
    
    addOnlineUser(username: string) {
      if (username && !this.onlineUsers.includes(username)) {
        this.onlineUsers.push(username);
      }
    },
    
    removeOnlineUser(username: string) {
      this.onlineUsers = this.onlineUsers.filter(user => user !== username);
    },
    
    clearMessages() {
      this.messages = [];
      this.stats.totalMessages = 0;
    },
    
    markMessagesAsRead(messageIds: string[]) {
      this.messages.forEach(msg => {
        if (messageIds.includes(msg.id || '')) {
          msg.read = true;
        }
      });
    }
  },
  
  // Pinia 持久化插件配置
  persist: {
    key: 'websocket-store',
    storage: localStorage,
    paths: ['currentRoom', 'stats']
  }
});
```

*📖 下一节：[ACK 消息确认机制](#-ack-消息确认机制)*

## 🔔 ACK 消息确认机制

### TypeScript ACK 管理器

```typescript
/**
 * ACK 消息确认管理器
 * 提供可靠的消息传输保证
 */
export class ACKManager {
  private client: AdvancedWebSocketClient;
  private pendingACKs = new Map<string, {
    resolve: (ack: WSMessage) => void;
    reject: (error: Error) => void;
    timeout: NodeJS.Timeout;
    message: WSMessage;
    retryCount: number;
  }>();
  
  private config: ACKConfig;
  
  constructor(client: AdvancedWebSocketClient, config: Partial<ACKConfig> = {}) {
    this.client = client;
    this.config = {
      timeout: 30000,          // 30秒超时
      maxRetries: 3,           // 最大重试3次
      retryInterval: 5000,     // 5秒重试间隔
      retryBackoffFactor: 2.0, // 重试间隔递增因子
      enableOfflineQueue: true, // 启用离线队列
      ...config
    };
    
    this.setupACKHandler();
  }
  
  /**
   * 设置 ACK 消息处理器
   */
  private setupACKHandler(): void {
    this.client.on('message', (message: WSMessage) => {
      if (message.type === 'ack') {
        this.handleACKResponse(message);
      }
    });
    
    this.client.on('disconnected', () => {
      this.handleDisconnection();
    });
  }
  
  /**
   * 发送需要确认的消息
   */
  public sendACKMessage(message: WSMessage): Promise<WSMessage> {
    return new Promise((resolve, reject) => {
      // 生成消息ID（如果没有）
      if (!message.id) {
        message.id = this.generateMessageId();
      }
      
      // 添加ACK要求标志
      message.requireACK = true;
      message.timestamp = Date.now();
      
      // 设置超时处理
      const timeoutId = setTimeout(() => {
        this.handleACKTimeout(message.id!);
      }, this.config.timeout);
      
      // 存储待确认消息
      this.pendingACKs.set(message.id, {
        resolve,
        reject,
        timeout: timeoutId,
        message,
        retryCount: 0
      });
      
      // 发送消息
      this.client.sendMessage(message.type, message.data, false)
        .then((sentId) => {
          console.log(`📤 ACK消息已发送: ${message.id}`);
          this.client.emit('ackMessageSent', message);
        })
        .catch((error) => {
          // 发送失败，清理并拒绝
          this.cleanupPendingACK(message.id!);
          reject(new Error(`发送失败: ${error.message}`));
        });
    });
  }
  
  /**
   * 发送批量ACK消息
   */
  public async sendBatchACKMessages(messages: WSMessage[]): Promise<WSMessage[]> {
    const promises = messages.map(msg => this.sendACKMessage(msg));
    return Promise.all(promises);
  }
  
  /**
   * 处理 ACK 响应
   */
  private handleACKResponse(ackMessage: WSMessage): void {
    const ackData = ackMessage.data as any;
    const originalMessageId = ackData?.messageId || ackData?.id;
    
    if (!originalMessageId) {
      console.warn('⚠️ 收到无效的ACK响应:', ackMessage);
      return;
    }
    
    const pending = this.pendingACKs.get(originalMessageId);
    if (pending) {
      // 清理超时定时器
      clearTimeout(pending.timeout);
      
      // 移除待确认消息
      this.pendingACKs.delete(originalMessageId);
      
      // 处理ACK状态
      if (ackData.status === 'success') {
        console.log(`✅ 消息确认成功: ${originalMessageId}`);
        pending.resolve(ackMessage);
        this.client.emit('ackReceived', ackMessage);
      } else {
        console.warn(`❌ 消息确认失败: ${originalMessageId}, 原因: ${ackData.reason}`);
        const error = new Error(`ACK失败: ${ackData.reason || '未知错误'}`);
        pending.reject(error);
        this.client.emit('ackFailed', originalMessageId, error);
      }
    } else {
      console.warn(`⚠️ 收到未知消息的ACK: ${originalMessageId}`);
    }
  }
  
  /**
   * 处理 ACK 超时
   */
  private async handleACKTimeout(messageId: string): Promise<void> {
    const pending = this.pendingACKs.get(messageId);
    if (!pending) return;
    
    console.warn(`⏰ ACK超时: ${messageId}, 重试次数: ${pending.retryCount}`);
    
    // 检查是否需要重试
    if (pending.retryCount < this.config.maxRetries) {
      await this.retryACKMessage(messageId);
    } else {
      // 达到最大重试次数，失败
      this.cleanupPendingACK(messageId);
      const error = new Error(`ACK超时，已达最大重试次数: ${this.config.maxRetries}`);
      pending.reject(error);
      this.client.emit('ackTimeout', messageId);
    }
  }
  
  /**
   * 重试 ACK 消息
   */
  private async retryACKMessage(messageId: string): Promise<void> {
    const pending = this.pendingACKs.get(messageId);
    if (!pending) return;
    
    // 增加重试次数
    pending.retryCount++;
    
    // 计算重试延迟（指数退避）
    const retryDelay = this.config.retryInterval * 
                      Math.pow(this.config.retryBackoffFactor, pending.retryCount - 1);
    
    console.log(`🔄 将在 ${retryDelay}ms 后重试发送消息: ${messageId} (第${pending.retryCount}次重试)`);
    
    // 延迟重试
    setTimeout(async () => {
      try {
        // 重新发送消息
        await this.client.sendMessage(pending.message.type, pending.message.data, false);
        
        // 重新设置超时
        clearTimeout(pending.timeout);
        pending.timeout = setTimeout(() => {
          this.handleACKTimeout(messageId);
        }, this.config.timeout);
        
        console.log(`📤 重试发送ACK消息: ${messageId}`);
        this.client.emit('ackMessageRetried', pending.message, pending.retryCount);
        
      } catch (error) {
        console.error(`❌ 重试发送失败: ${messageId}`, error);
        this.cleanupPendingACK(messageId);
        pending.reject(new Error(`重试发送失败: ${error.message}`));
      }
    }, retryDelay);
  }
  
  /**
   * 处理连接断开
   */
  private handleDisconnection(): void {
    if (!this.config.enableOfflineQueue) {
      // 不支持离线队列，直接失败所有待确认消息
      for (const [messageId, pending] of this.pendingACKs.entries()) {
        clearTimeout(pending.timeout);
        pending.reject(new Error('连接断开'));
      }
      this.pendingACKs.clear();
      console.log('🔌 连接断开，清理所有待确认消息');
    } else {
      // 支持离线队列，暂停超时计时器
      for (const [messageId, pending] of this.pendingACKs.entries()) {
        clearTimeout(pending.timeout);
        console.log(`⏸️ 暂停消息超时计时: ${messageId}`);
      }
      
      // 监听重连事件
      this.client.once('connected', () => {
        this.resumePendingACKs();
      });
    }
  }
  
  /**
   * 恢复待确认消息
   */
  private resumePendingACKs(): void {
    console.log(`🔄 连接恢复，恢复 ${this.pendingACKs.size} 个待确认消息`);
    
    for (const [messageId, pending] of this.pendingACKs.entries()) {
      // 重新设置超时计时器
      pending.timeout = setTimeout(() => {
        this.handleACKTimeout(messageId);
      }, this.config.timeout);
      
      console.log(`▶️ 恢复消息超时计时: ${messageId}`);
    }
  }
  
  /**
   * 手动发送 ACK 确认
   */
  public sendACKResponse(messageId: string, status: 'success' | 'failed', reason?: string): Promise<void> {
    const ackMessage: WSMessage = {
      id: this.generateMessageId(),
      type: 'ack',
      data: {
        messageId,
        status,
        reason,
        timestamp: Date.now()
      },
      timestamp: Date.now()
    };
    
    return this.client.sendJSON(ackMessage);
  }
  
  /**
   * 清理待确认消息
   */
  private cleanupPendingACK(messageId: string): void {
    const pending = this.pendingACKs.get(messageId);
    if (pending) {
      clearTimeout(pending.timeout);
      this.pendingACKs.delete(messageId);
    }
  }
  
  /**
   * 生成消息ID
   */
  private generateMessageId(): string {
    return `ack-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
  }
  
  /**
   * 获取待确认消息统计
   */
  public getACKStats(): ACKStats {
    return {
      pendingCount: this.pendingACKs.size,
      pendingIds: Array.from(this.pendingACKs.keys()),
      config: { ...this.config }
    };
  }
  
  /**
   * 清理所有待确认消息
   */
  public clearAllPendingACKs(): void {
    for (const [messageId, pending] of this.pendingACKs.entries()) {
      clearTimeout(pending.timeout);
      pending.reject(new Error('手动清理'));
    }
    this.pendingACKs.clear();
    console.log('🧹 已清理所有待确认消息');
  }
}

/**
 * ACK 配置接口
 */
export interface ACKConfig {
  timeout: number;                // 超时时间 (毫秒)
  maxRetries: number;            // 最大重试次数
  retryInterval: number;         // 重试间隔 (毫秒)
  retryBackoffFactor: number;    // 重试间隔递增因子
  enableOfflineQueue: boolean;   // 启用离线队列
}

/**
 * ACK 统计信息
 */
export interface ACKStats {
  pendingCount: number;
  pendingIds: string[];
  config: ACKConfig;
}

/**
 * 扩展 WSMessage 接口
 */
declare module './websocket-client' {
  interface WSMessage {
    requireACK?: boolean;
    read?: boolean;
  }
}
```

### React Hook with ACK

```typescript
// hooks/useWebSocketACK.ts
import { useState, useCallback, useRef } from 'react';
import { useWebSocket } from './useWebSocket';
import { ACKManager } from '../ack-manager';

export interface UseWebSocketACKOptions {
  serverUrl: string;
  ackConfig?: Partial<ACKConfig>;
  onACKReceived?: (ack: WSMessage) => void;
  onACKTimeout?: (messageId: string) => void;
  onACKFailed?: (messageId: string, error: Error) => void;
}

export function useWebSocketACK(options: UseWebSocketACKOptions) {
  const {
    serverUrl,
    ackConfig = {},
    onACKReceived,
    onACKTimeout,
    onACKFailed
  } = options;
  
  const [ackStats, setACKStats] = useState<ACKStats>({
    pendingCount: 0,
    pendingIds: [],
    config: {
      timeout: 30000,
      maxRetries: 3,
      retryInterval: 5000,
      retryBackoffFactor: 2.0,
      enableOfflineQueue: true,
      ...ackConfig
    }
  });
  
  const ackManagerRef = useRef<ACKManager | null>(null);
  
  const {
    isConnected,
    isConnecting,
    error,
    sendMessage: baseSendMessage,
    sendText: baseSendText,
    connect,
    disconnect
  } = useWebSocket(serverUrl, {
    immediate: true,
    onConnect: () => {
      if (!ackManagerRef.current) {
        // 这里需要在 useWebSocket 中暴露 client 实例
        // 或者重新设计架构
      }
    }
  });
  
  // 发送需要确认的消息
  const sendACKMessage = useCallback(async (type: string, data: any): Promise<WSMessage> => {
    if (!ackManagerRef.current) {
      throw new Error('ACK Manager 未初始化');
    }
    
    const message: WSMessage = {
      type,
      data,
      timestamp: Date.now()
    };
    
    try {
      const ack = await ackManagerRef.current.sendACKMessage(message);
      setACKStats(prev => ({
        ...prev,
        pendingCount: ackManagerRef.current!.getACKStats().pendingCount
      }));
      return ack;
    } catch (error) {
      setACKStats(prev => ({
        ...prev,
        pendingCount: ackManagerRef.current!.getACKStats().pendingCount
      }));
      throw error;
    }
  }, []);
  
  // 发送批量确认消息
  const sendBatchACKMessages = useCallback(async (messages: Array<{type: string, data: any}>): Promise<WSMessage[]> => {
    if (!ackManagerRef.current) {
      throw new Error('ACK Manager 未初始化');
    }
    
    const wsMessages: WSMessage[] = messages.map(msg => ({
      type: msg.type,
      data: msg.data,
      timestamp: Date.now()
    }));
    
    return ackManagerRef.current.sendBatchACKMessages(wsMessages);
  }, []);
  
  // 发送ACK响应
  const sendACKResponse = useCallback(async (messageId: string, status: 'success' | 'failed', reason?: string): Promise<void> => {
    if (!ackManagerRef.current) {
      throw new Error('ACK Manager 未初始化');
    }
    
    return ackManagerRef.current.sendACKResponse(messageId, status, reason);
  }, []);
  
  // 清理待确认消息
  const clearPendingACKs = useCallback(() => {
    if (ackManagerRef.current) {
      ackManagerRef.current.clearAllPendingACKs();
      setACKStats(prev => ({
        ...prev,
        pendingCount: 0,
        pendingIds: []
      }));
    }
  }, []);
  
  // 获取ACK统计
  const getACKStats = useCallback(() => {
    return ackManagerRef.current?.getACKStats() || ackStats;
  }, [ackStats]);
  
  return {
    // 基础连接状态
    isConnected,
    isConnecting,
    error,
    connect,
    disconnect,
    
    // ACK 相关方法
    sendACKMessage,
    sendBatchACKMessages,
    sendACKResponse,
    clearPendingACKs,
    getACKStats,
    
    // ACK 统计
    ackStats,
    
    // 兼容性方法（不需要确认）
    sendMessage: baseSendMessage,
    sendText: baseSendText
  };
}
```

### Vue 3 ACK 组合式函数

```typescript
// composables/useWebSocketACK.ts
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue';
import { useWebSocket } from './useWebSocket';
import { ACKManager, ACKConfig, ACKStats } from '../ack-manager';

export function useWebSocketACK(
  url: string, 
  ackConfig: Partial<ACKConfig> = {}
) {
  const ackStats = ref<ACKStats>({
    pendingCount: 0,
    pendingIds: [],
    config: {
      timeout: 30000,
      maxRetries: 3,
      retryInterval: 5000,
      retryBackoffFactor: 2.0,
      enableOfflineQueue: true,
      ...ackConfig
    }
  });
  
  const ackManager = ref<ACKManager | null>(null);
  
  // 基础 WebSocket 功能
  const {
    isConnected,
    isConnecting,
    error,
    messages,
    connect: baseConnect,
    disconnect: baseDisconnect,
    on,
    off
  } = useWebSocket(url, {
    immediate: false, // 手动控制连接
    onConnect: () => {
      console.log('✅ Vue ACK WebSocket 连接成功');
    }
  });
  
  // 初始化 ACK 管理器
  const initACKManager = () => {
    // 这里需要访问 WebSocket 客户端实例
    // 实际实现中需要修改 useWebSocket 暴露客户端实例
  };
  
  // 连接方法（包含 ACK 初始化）
  const connect = async () => {
    await baseConnect();
    initACKManager();
  };
  
  // 断开连接
  const disconnect = () => {
    if (ackManager.value) {
      ackManager.value.clearAllPendingACKs();
    }
    baseDisconnect();
  };
  
  // 发送需要确认的消息
  const sendACKMessage = async (type: string, data: any): Promise<WSMessage> => {
    if (!ackManager.value) {
      throw new Error('ACK Manager 未初始化');
    }
    
    const message: WSMessage = {
      type,
      data,
      timestamp: Date.now()
    };
    
    try {
      const ack = await ackManager.value.sendACKMessage(message);
      updateACKStats();
      return ack;
    } catch (error) {
      updateACKStats();
      throw error;
    }
  };
  
  // 更新 ACK 统计
  const updateACKStats = () => {
    if (ackManager.value) {
      ackStats.value = ackManager.value.getACKStats();
    }
  };
  
  // 计算属性
  const hasPendingACKs = computed(() => ackStats.value.pendingCount > 0);
  
  const ackSuccessRate = computed(() => {
    const total = messages.value.filter(m => m.requireACK).length;
    const failed = ackStats.value.pendingIds.length;
    return total > 0 ? ((total - failed) / total) * 100 : 100;
  });
  
  // 生命周期
  onMounted(() => {
    // 设置 ACK 相关事件监听
    on('ackReceived', (ack: WSMessage) => {
      updateACKStats();
      console.log('✅ ACK 确认收到:', ack);
    });
    
    on('ackTimeout', (messageId: string) => {
      updateACKStats();
      console.warn('⏰ ACK 超时:', messageId);
    });
    
    on('ackFailed', (messageId: string, error: Error) => {
      updateACKStats();
      console.error('❌ ACK 失败:', messageId, error);
    });
  });
  
  onUnmounted(() => {
    disconnect();
  });
  
  return {
    // 基础状态
    isConnected,
    isConnecting,
    error,
    messages,
    
    // ACK 功能
    sendACKMessage,
    ackStats: readonly(ackStats),
    hasPendingACKs,
    ackSuccessRate,
    
    // 连接控制
    connect,
    disconnect,
    
    // 事件处理
    on,
    off
  };
}
```

### 实际应用示例：可靠聊天系统

```typescript
// 可靠聊天系统示例
class ReliableChatSystem {
  private wsClient: AdvancedWebSocketClient;
  private ackManager: ACKManager;
  private messageStore: Map<string, any> = new Map();
  
  constructor(serverUrl: string) {
    this.wsClient = new AdvancedWebSocketClient(serverUrl);
    this.ackManager = new ACKManager(this.wsClient, {
      timeout: 15000,
      maxRetries: 5,
      retryInterval: 3000,
      enableOfflineQueue: true
    });
    
    this.setupEventHandlers();
  }
  
  private setupEventHandlers() {
    // 处理普通消息接收
    this.wsClient.on('message', (message: WSMessage) => {
      if (message.type === 'chat' && message.id) {
        // 自动发送ACK确认
        this.ackManager.sendACKResponse(message.id, 'success')
          .then(() => console.log(`📨 已确认接收消息: ${message.id}`))
          .catch(err => console.error('确认失败:', err));
        
        // 处理聊天消息
        this.handleChatMessage(message);
      }
    });
    
    // 处理ACK确认
    this.wsClient.on('ackReceived', (ack: WSMessage) => {
      console.log('✅ 消息已送达:', ack);
      this.markMessageAsDelivered(ack.data.messageId);
    });
    
    // 处理ACK超时
    this.wsClient.on('ackTimeout', (messageId: string) => {
      console.warn('⏰ 消息送达超时:', messageId);
      this.markMessageAsFailed(messageId);
    });
  }
  
  /**
   * 发送聊天消息（需要确认）
   */
  public async sendChatMessage(content: string, to: string): Promise<string> {
    const messageId = this.generateMessageId();
    const message: WSMessage = {
      id: messageId,
      type: 'chat',
      data: {
        content,
        to,
        from: 'currentUser',
        timestamp: Date.now()
      }
    };
    
    // 存储消息状态
    this.messageStore.set(messageId, {
      ...message,
      status: 'sending',
      attempts: 0
    });
    
    try {
      await this.ackManager.sendACKMessage(message);
      this.updateMessageStatus(messageId, 'sent');
      console.log(`📤 聊天消息已发送: ${messageId}`);
      return messageId;
    } catch (error) {
      this.updateMessageStatus(messageId, 'failed');
      console.error('❌ 发送聊天消息失败:', error);
      throw error;
    }
  }
  
  /**
   * 批量发送消息
   */
  public async sendBatchMessages(messages: Array<{content: string, to: string}>): Promise<string[]> {
    const wsMessages: WSMessage[] = messages.map(msg => ({
      id: this.generateMessageId(),
      type: 'chat',
      data: {
        ...msg,
        from: 'currentUser',
        timestamp: Date.now()
      }
    }));
    
    // 存储所有消息状态
    wsMessages.forEach(msg => {
      this.messageStore.set(msg.id!, {
        ...msg,
        status: 'sending',
        attempts: 0
      });
    });
    
    try {
      await this.ackManager.sendBatchACKMessages(wsMessages);
      const messageIds = wsMessages.map(msg => msg.id!);
      messageIds.forEach(id => this.updateMessageStatus(id, 'sent'));
      return messageIds;
    } catch (error) {
      wsMessages.forEach(msg => this.updateMessageStatus(msg.id!, 'failed'));
      throw error;
    }
  }
  
  private handleChatMessage(message: WSMessage) {
    console.log('💬 收到聊天消息:', message.data.content);
    // 处理接收到的聊天消息
  }
  
  private markMessageAsDelivered(messageId: string) {
    this.updateMessageStatus(messageId, 'delivered');
  }
  
  private markMessageAsFailed(messageId: string) {
    this.updateMessageStatus(messageId, 'failed');
  }
  
  private updateMessageStatus(messageId: string, status: string) {
    const message = this.messageStore.get(messageId);
    if (message) {
      message.status = status;
      message.lastUpdated = Date.now();
      this.messageStore.set(messageId, message);
    }
  }
  
  private generateMessageId(): string {
    return `chat-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
  }
  
  /**
   * 获取消息统计
   */
  public getMessageStats() {
    const messages = Array.from(this.messageStore.values());
    return {
      total: messages.length,
      sent: messages.filter(m => m.status === 'sent').length,
      delivered: messages.filter(m => m.status === 'delivered').length,
      failed: messages.filter(m => m.status === 'failed').length,
      sending: messages.filter(m => m.status === 'sending').length
    };
  }
  
  /**
   * 重发失败的消息
   */
  public async resendFailedMessages(): Promise<void> {
    const failedMessages = Array.from(this.messageStore.values())
      .filter(msg => msg.status === 'failed');
    
    for (const msg of failedMessages) {
      try {
        await this.ackManager.sendACKMessage(msg);
        this.updateMessageStatus(msg.id, 'sent');
      } catch (error) {
        console.error(`重发消息失败: ${msg.id}`, error);
      }
    }
  }
}

// 使用示例
const chatSystem = new ReliableChatSystem('ws://localhost:8080/ws');

// 发送单条消息
chatSystem.sendChatMessage('Hello, World!', 'user123')
  .then(messageId => console.log('消息已发送:', messageId))
  .catch(error => console.error('发送失败:', error));

// 发送批量消息
chatSystem.sendBatchMessages([
  { content: 'Message 1', to: 'user1' },
  { content: 'Message 2', to: 'user2' },
  { content: 'Message 3', to: 'user3' }
]).then(messageIds => {
  console.log('批量消息已发送:', messageIds);
});

// 查看消息统计
console.log('消息统计:', chatSystem.getMessageStats());
```
