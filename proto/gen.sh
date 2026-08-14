#!/usr/bin/env bash
# ============================================================================
# @Author: kamalyes 501893067@qq.com
# @Date: 2026-07-18 00:00:00
# @LastEditTime: 2026-07-18 00:00:00
# @FilePath: \go-wsc\proto\gen.sh
# @Description: protoc 生成脚本 - 由 proto 文件生成 Go protobuf 代码
#
# 用法:
#   ./gen.sh              # 生成代码
#   ./gen.sh --check      # 仅检查工具链，不生成
#
# 依赖:
#   - protoc                (protobuf 编译器)
#   - protoc-gen-go         (Go protobuf 插件，缺失时自动安装)
#   - protoc-gen-go-grpc    (Go gRPC 插件，缺失时自动安装)
#
# Copyright (c) 2026 by kamalyes, All Rights Reserved.
# ============================================================================

set -euo pipefail

# 脚本所在目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# 项目根目录（proto 目录的上一级）
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$PROJECT_ROOT"

# ---------- 颜色输出 ----------
if [[ -t 1 ]]; then
    COLOR_CYAN='\033[36m'
    COLOR_GREEN='\033[32m'
    COLOR_YELLOW='\033[33m'
    COLOR_RED='\033[31m'
    COLOR_RESET='\033[0m'
else
    COLOR_CYAN=''; COLOR_GREEN=''; COLOR_YELLOW=''; COLOR_RED=''; COLOR_RESET=''
fi

log_info()  { printf "${COLOR_CYAN}==> %s${COLOR_RESET}\n" "$*"; }
log_ok()    { printf "${COLOR_GREEN}✔ %s${COLOR_RESET}\n" "$*"; }
log_warn()  { printf "${COLOR_YELLOW}! %s${COLOR_RESET}\n" "$*"; }
log_error() { printf "${COLOR_RED}✖ %s${COLOR_RESET}\n" "$*" >&2; }

# ---------- 工具链检查 ----------
check_protoc() {
    if ! command -v protoc &> /dev/null; then
        log_error "未找到 protoc，请先安装 protobuf 编译器"
        echo "  安装方法: https://grpc.io/docs/protoc-installation/"
        echo "  macOS:   brew install protobuf"
        echo "  Ubuntu:  sudo apt install -y protobuf-compiler"
        exit 1
    fi
    log_ok "protoc: $(protoc --version)"
}

check_protoc_gen_go() {
    if ! command -v protoc-gen-go &> /dev/null; then
        log_warn "未找到 protoc-gen-go，正在安装..."
        go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
        # 确保 GOBIN 在 PATH 中
        local gobin
        gobin="$(go env GOBIN)"
        [[ -z "$gobin" ]] && gobin="$(go env GOPATH)/bin"
        if [[ ":$PATH:" != *":$gobin:"* ]]; then
            export PATH="$gobin:$PATH"
        fi
    fi
    log_ok "protoc-gen-go: $(protoc-gen-go --version 2>&1)"
}

check_protoc_gen_go_grpc() {
    if ! command -v protoc-gen-go-grpc &> /dev/null; then
        log_warn "未找到 protoc-gen-go-grpc，正在安装..."
        go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
        local gobin
        gobin="$(go env GOBIN)"
        [[ -z "$gobin" ]] && gobin="$(go env GOPATH)/bin"
        if [[ ":$PATH:" != *":$gobin:"* ]]; then
            export PATH="$gobin:$PATH"
        fi
    fi
    log_ok "protoc-gen-go-grpc: $(protoc-gen-go-grpc --version 2>&1)"
}

# ---------- 生成单个 proto 文件 ----------
# 用法: generate_proto <proto_file> <has_grpc>
#   proto_file: 相对项目根目录的 proto 文件路径
#   has_grpc:  "yes" 或 "no"，是否需要生成 gRPC 代码
generate_proto() {
    local proto_file="$1"
    local has_grpc="${2:-no}"
    local base_name
    base_name="$(basename "$proto_file" .proto)"

    if [[ ! -f "$proto_file" ]]; then
        log_error "未找到 proto 文件: $proto_file"
        return 1
    fi

    log_info "源文件: $proto_file"

    if [[ "$has_grpc" == "yes" ]]; then
        protoc \
            -I "$PROTO_DIR" \
            --go_out="$OUT_DIR" \
            --go_opt=paths=source_relative \
            --go-grpc_out="$OUT_DIR" \
            --go-grpc_opt=paths=source_relative \
            "$proto_file"
        log_ok "生成完成: $OUT_DIR/${base_name}.pb.go  $OUT_DIR/${base_name}_grpc.pb.go"
        GENERATED_FILES+=("$OUT_DIR/${base_name}.pb.go" "$OUT_DIR/${base_name}_grpc.pb.go")
    else
        protoc \
            -I "$PROTO_DIR" \
            --go_out="$OUT_DIR" \
            --go_opt=paths=source_relative \
            "$proto_file"
        log_ok "生成完成: $OUT_DIR/${base_name}.pb.go"
        GENERATED_FILES+=("$OUT_DIR/${base_name}.pb.go")
    fi
}

# ---------- 主流程 ----------
PROTO_DIR="proto"
OUT_DIR="models/pb"

# proto 文件列表: "文件路径:是否含gRPC"
PROTO_FILES=(
    "proto/wsc.proto:no"
    "proto/node.proto:yes"
)

# 工具链检查
log_info "检查工具链..."
check_protoc
check_protoc_gen_go
check_protoc_gen_go_grpc

# 仅检查模式
if [[ "${1:-}" == "--check" ]]; then
    log_ok "工具链检查通过"
    exit 0
fi

# 生成代码
log_info "生成 protobuf 代码..."
log_info "输出目录: $OUT_DIR"

mkdir -p "$OUT_DIR"

# 收集生成的文件，用于后处理
GENERATED_FILES=()

for entry in "${PROTO_FILES[@]}"; do
    IFS=':' read -r proto_file has_grpc <<< "$entry"
    generate_proto "$proto_file" "$has_grpc"
done

# 后处理: 格式化生成的代码
if command -v gofmt &> /dev/null; then
    log_info "执行 gofmt..."
    gofmt -w "${GENERATED_FILES[@]}"
    log_ok "gofmt 完成"
fi

# 后处理: 整理依赖
if command -v goimports &> /dev/null; then
    log_info "执行 goimports..."
    goimports -w "${GENERATED_FILES[@]}"
    log_ok "goimports 完成"
fi

log_ok "全部完成"
