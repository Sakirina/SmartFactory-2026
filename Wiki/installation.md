# 安装部署

[Wiki 首页](README.md) · [快速开始](getting-started.md)

持久部署使用 Linux x86_64、Python 3.12 及以上版本和 Docker Compose。程序归档提供 SmartFactory 二进制与页面，数据库、原生平台和消息服务采用锁定镜像；安装工具在私有目录中生成运行配置、随机凭据、客户 CA 和节点身份。

## 选择部署形态

| 准备参数 | 生成内容 |
| --- | --- |
| `--profile capacity --simulation scenes` | 一个边缘节点、五个示例场景 |
| `--profile capacity --simulation fleet` | 一个边缘节点、500 台模拟设备 |
| `--profile site --simulation fleet` | 三个边缘节点，每节点 50 台模拟设备，三个 NATS 副本 |
| `--hosting self` 或 `--hosting hosted` | 在部署信息中记录自托管或托管运维归属 |

`simulation` 默认为 `fleet`，`site` 使用该模式。资源规划需要计入原生 CE、每个 Edge、数据库、队列、归档与备份；单节点目标环境的初始规划为 8 vCPU、16 GB 内存和 220 GB 磁盘，三节点按实际服务数和保留期另行测量。UOS Server V20 x86_64 的完整安装恢复与长时间运行安排见[版本说明](releases/1.1.md)。

## 准备部署包和镜像

1. 从[发布页](https://github.com/Sakirina/SmartFactory-2026/releases)下载同一版本的归档及校验文件。以 `v1.1.0` 为例，在下载目录执行：

   ```sh
   sha256sum -c smartfactory-v1.1.0-linux-amd64.tar.gz.sha256
   tar -xzf smartfactory-v1.1.0-linux-amd64.tar.gz
   cd smartfactory-v1.1.0-linux-amd64
   python3 scripts/verify-release.py .
   ```

   后续命令在解包后的根目录执行。校验工具核对包内文件集合、大小和摘要，部署后保留这个目录供 Compose 挂载。

2. 确认工具可以运行，再查看镜像清单与传输规模：

   ```sh
   python3 --version
   docker version
   docker compose version
   python3 scripts/pull-images.py
   ```

   按主机缓存和网络条件准备镜像，执行获取使用 `python3 scripts/pull-images.py --execute`，选择部分镜像使用 `--only`。生成的 Compose 使用 `pull_policy: never`，安装读取本地已经准备的锁定镜像；离线主机需要提前加载对应镜像归档。

3. 准备当前账号可写的新私有目录和充足空间，确定浏览器实际访问的主机名。下面使用本机地址；远程部署把 `localhost` 改为实际域名，并配置域名解析。

## 安装五场景持久实例

1. 生成私有状态和 Compose：

   ```sh
   python3 scripts/prepare-runtime.py \
     --bundle "$PWD" \
     --directory ../smartfactory-state \
     --profile capacity \
     --hosting self \
     --simulation scenes \
     --public-host localhost
   ```

   `--directory` 必须是新目录，工具创建证书、主密钥、工作负载凭据和初始数据库配置，并检查 Compose。三节点把参数改为 `--profile site --simulation fleet`。

2. 初始化数据库、原生平台、云边登记和模拟设备：

   ```sh
   python3 scripts/bootstrap-runtime.py install --directory ../smartfactory-state
   ```

   工具等待 CE／Edge 同步、权限包和定义投递，再启动模拟与采集、批准示例设备。安装记录保存到 `installed.json`，进度需要结合当前组件的启动日志查看。

3. 在浏览器的信任存储中导入私有目录里的 `pki/ca.pem`，访问 `profile.json` 记录的地址。使用 `admin` 进行初始管理，或使用 `viewer` 查看示例；初始密码读取 `deployment-credentials.json` 的 `password` 字段。

   | 页面 | 单节点默认地址 |
   | --- | --- |
   | 云端控制台 | `https://localhost:8443/` |
   | 现场工作台 | `https://localhost:8444/edge` |
   | 管理大屏 | `https://localhost:8443/screen` |

   三节点的另外两个现场入口使用 HTTPS 8445 和 8446。云端和现场服务的回环调试端口分别为 8090、8091／8093／8094，完整端口用途见[配置参考](reference/configuration.md)。

4. 在“运行状态”核对节点心跳与队列，在“设备与资产”确认示例已批准，再完成[首个业务流程](getting-started.md)与[五场景检查](tutorials/five-scenes.md)。实际使用前登记人员、资源和独立二次认证资料，操作见[组织与权限](user-guide/organization.md)。

安装形成普通 Edge 实例。需要采用不可变程序、规则和配置时，继续[节点发布指南](user-guide/node-releases.md)，先登记工件和创建批次，再执行 `prepare-release` 与 `enable-release`。

## 启停和已转换节点

```sh
python3 scripts/bootstrap-runtime.py status --directory ../smartfactory-state
python3 scripts/bootstrap-runtime.py stop --directory ../smartfactory-state
python3 scripts/bootstrap-runtime.py start --directory ../smartfactory-state
```

这些命令读取同一私有目录，包含原生 Edge 与发布代理的 Compose profile；`start` 根据 `release_managed_nodes` 启动已经转换的代理。直接使用 Compose 检查代理服务时加上 `--profile release`，例如：

```sh
docker compose -f ../smartfactory-state/compose.json \
  --profile native-edge \
  --profile release ps
```

PostgreSQL 18 使用新的 `*-pg18` 卷并挂载 `/var/lib/postgresql`，官方镜像的数据目录位于其下的 `18/docker`。已有 PostgreSQL 16 数据按[备份与恢复](operations/backup-restore.md)建立独立目标，再执行逻辑恢复；正常重启继续使用原卷和原私有文件。

安装失败时保留生成目录，按[故障排查](operations/troubleshooting.md)检查已记录初始化阶段与失败服务。完整备份同时保存业务及原生数据库、消息卷、采集状态、凭据和密钥。

本页依据：[部署生成器](../scripts/prepare-runtime.py)、[初始化及启停](../scripts/bootstrap-runtime.py)、[镜像工具](../scripts/pull-images.py)、[镜像锁](../deploy/images.lock.json)。
