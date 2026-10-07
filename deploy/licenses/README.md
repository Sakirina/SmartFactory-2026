# 组件来源与许可材料

[dependencies.json](../dependencies.json)记录主程序版本、来源标签和提交，[images.lock.json](../images.lock.json)记录实际准备的镜像摘要与平台；本目录中的原文及[provenance.json](provenance.json)提供对应文件的来源和 SHA-256。部署包另有 `components.json` 与 `licenses/`，按实际 Go、npm 和镜像组件建立对应关系。

镜像按锁定摘要另行准备。下表中的许可文件适用于所列主程序或材料，镜像内的操作系统、打包依赖及其他组件各自采用所属项目的许可；准备镜像时同时保留发行者提供的包清单、源码和许可资料。

| 组件 | 对应版本或范围 | 包内材料 |
| --- | --- | --- |
| ThingsBoard CE | `v4.3.1.6` 的固定源码提交 | 主程序与 `ui-ngx` 的 `LICENSE` |
| ThingsBoard Edge | `4.3.1.6EDGE` 对应的固定源码提交 | 主程序与 `ui-ngx` 的 `LICENSE` |
| PostgreSQL | 上游 `REL_18_6`，镜像内程序报告 `18.6` | 上游 `COPYRIGHT` |
| Kafka | `4.3.1` | 上游 `LICENSE` 和 `NOTICE` |
| NATS | 目标 `2.14.7` 与升级中间版 `2.12.15` | 各自固定提交的 `LICENSE` |
| Envoy | `v1.39.1`，版本输出中的提交与来源标签一致 | 上游 `LICENSE` 和 `NOTICE` |
| Go 构建镜像 | `go1.27.1` | 上游 `LICENSE`；实际编译器及标准库另外作为 `go@1.27.1` 组件记录 |
| Node 构建镜像 | `v24.21.0` | 上游 `LICENSE`，其中包含 Node 项目列出的第三方许可材料 |
| Alpine 运行镜像 | 锁定 `linux/amd64` 镜像内的 `3.22.6` | 16 个实际安装包的许可表达式、来源提交、10 份源码配方及对应许可文本 |

Alpine 的[包材料索引](runtime/packages.json)来自锁定镜像的 `/lib/apk/db/installed`。每个安装包记录版本、许可表达式、来源项目与实际 `aports` 提交，源码配方按该提交固定；GPL-2.0、MPL-2.0、musl、OpenSSL 和 zlib 的文本分别列在适用包下。MIT 声明和包内补丁的具体来源保存在相应源码配方中，完整源码的获取地址与摘要也由配方记录。

npm 材料采用锁文件中的实际发行身份，安装别名和安装位置另行记录。`uri-js-replace@1.0.1` 的发行包及对应源码提交提供 MIT 声明，包内保存其原 `package.json` 和来源摘要；该材料的形式在组件记录中注明。Go 模块中的项目自有源码与第三方模块分别标识，第三方许可保持原文。
