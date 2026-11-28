# 第三方软件声明

## 1. sing-box 的版权与许可

SingBox Proxy Manager 自身代码采用 MIT 许可证。发行二进制包含以独立子进程运行的 `sing-box v1.12.12`（提交 `54ed58499d7063136ed52dabf87d179d252425d0`）。该组件由 nekohasekai 持有下列版权，并按 `GPL-3.0-or-later` 分发。管理器与内核分别适用各自的许可证。[管理器 MIT 许可证](LICENSE) · [GNU GPL v3 完整正文](licenses/SING-BOX-GPL-3.0.txt)

上游 `LICENSE` 原文如下。[上游许可证](https://github.com/SagerNet/sing-box/blob/54ed58499d7063136ed52dabf87d179d252425d0/LICENSE)

```text
Copyright (C) 2022 by nekohasekai <contact-sagernet@sekai.icu>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
```

## 2. 对应源码与重编译

所分发内核使用未经修改的官方 Go 模块 `github.com/sagernet/sing-box@v1.12.12` 重编译。构建脚本通过 `go mod download -json` 获取官方模块，验证归档校验和 `h1:brSb4zdL5CfqXB0ss1jrT+srPUbKanyWhbswkte/z5Y=`，再以 `-mod=readonly` 编译。 [官方仓库](https://github.com/SagerNet/sing-box) · [v1.12.12 标签](https://github.com/SagerNet/sing-box/tree/v1.12.12) · [固定提交](https://github.com/SagerNet/sing-box/commit/54ed58499d7063136ed52dabf87d179d252425d0) · [对应源码 tar.gz](https://github.com/SagerNet/sing-box/archive/54ed58499d7063136ed52dabf87d179d252425d0.tar.gz) · [构建脚本](scripts/build-sing-box.sh)

下载固定提交归档即可取得内核源码。归档中的 `go.mod` 和 `go.sum` 固定构建依赖，可在解包目录用 Go `1.26.11` 执行 `GOTOOLCHAIN=local GOWORK=off go mod download` 获取对应依赖源码。管理器发行标签中的 `scripts/build-sing-box.sh` 和 `scripts/verify-go-build.sh` 提供重编译及验证流程。从对应版本的管理器源码目录执行 `./scripts/build-sing-box.sh /绝对路径/sing-box`，按目标平台设置 `GOOS`、`GOARCH`。 [官方模块归档](https://proxy.golang.org/github.com/sagernet/sing-box/@v/v1.12.12.zip) · [Go 校验记录](https://sum.golang.org/lookup/github.com/sagernet/sing-box@v1.12.12)

内核入口为官方模块的 `./cmd/sing-box`，使用以下工具链、环境和编译参数。

| 项目 | 固定值 |
| --- | --- |
| Go | `1.26.11` |
| 环境变量 | `GOTOOLCHAIN=local`、`GOWORK=off`、`CGO_ENABLED=0` |
| 构建选项 | `-mod=readonly -trimpath -buildvcs=false` |
| build tags | `-tags with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_tailscale` |
| ldflags | `-ldflags "-X github.com/sagernet/sing-box/constant.Version=1.12.12 -s -w -buildid="` |

## 3. 嵌入式离线 IP 国家/地区数据

后端 `backend/services/ipgeo_data.bin` 仅用于「当前连接」面板的轻量离线客户端 IP 国家码识别，当前分块压缩二进制由 [`scripts/build-ipgeo-db.py`](scripts/build-ipgeo-db.py) 基于 [sapics/ip-location-db](https://github.com/sapics/ip-location-db) 的 `geo-whois-asn-country` 数据集编译生成，采用 [Creative Commons Zero v1.0 Universal (CC0-1.0)](https://creativecommons.org/publicdomain/zero/1.0/) 公共领域奉献分发（该脚本亦支持从上游 `user-country` PDDL-1.0 Release 重新编译更新）。

## 4. 关联声明

SingBox Proxy Manager 是独立项目，与 `sing-box`、`SagerNet`、`nekohasekai` 及 `sapics/ip-location-db` 没有官方隶属、赞助或背书关系。
