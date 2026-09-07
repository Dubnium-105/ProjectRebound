# 固定 NMT_Login field2 的静态所有权复核

本次只读取固定 EXE 和既有 IDB。EXE SHA-256 为 `181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`，SizeOfImage 为 `0x648c000`。没有启动或附加游戏，也没有运行在线或离线对局。原始 IDA 输出见 `ida-results.json`；独立 PE 字节、直接调用和虚表绑定检查见 `binary-binding.json`。

此前的静态报告停在 archive 虚调用，因而没有证明其是否保存输入指针。本次继续解析该固定调用图：

- field2 调用点 RVA `0x03484F35` 调用 `0x0189D040`。同一栈上 archive 在 `0x03484ED2` 经 `0x031CF3D0` 构造；构造函数把最终虚表写为 VA `0x145007AF0`。
- 该虚表 `+0x10` 指向 `0x14086AFF0`，原始字节为 `C2 00 00`，是空操作返回。serializer 向该入口传递长度和容量数值，没有传递 FString 对象或 Data 指针。
- 同一虚表 `+0x150` 指向 `0x1419CD9F0`。完整反汇编显示输入 Data 保存在 64 位 R15，再以 R8 传给 `0x1419D1A30`；IDA 推断出的 `int a2` 不能用作这个 ABI 的声明。
- writer 只扩容自己的 `archive+0x98` 缓冲、同步复制位并增加 `archive+0xA8` 写入位置。596 字节的 bit-copy 函数没有任何调用，只读取 source 并写入传入的 destination；没有保存 source 指针、排队或释放 source 的路径。writer 的容量错误分支只设置 archive 错误位。
- serializer 的 ASCII 分支先把 UTF-16 字符复制为自己拥有的编码临时区，然后同步写入并释放该临时区。窄字符转换函数只写 destination；其异常字符日志分支逐字符构造另一个本地字符串，不把输入指针交给日志对象保存。当前 Payload 拒绝中间 NUL，因此不会通过提前终止扫描掩藏后续非 ASCII 字符。
- Unicode 分支直接同步写入 source，或先复制到 serializer 自己拥有的交换字节序缓冲再写入。释放点针对 serializer 的临时区；保存分支不改写传入 FString 的 Data/Num/Max，也不释放它。原始 caller field2 仍由原始调用者在发送后释放，Payload 只在同步调用返回后清理自己拥有的 std::wstring。

据此，该固定调用图支持 **borrowed FString 在同步保存调用期间有效，原生代码不保留或释放该输入对象/Data** 的静态结论。这个结论限定于固定 EXE、固定 field2 调用点及其构造的 archive，不推广到加载分支、其他 FArchive 类型或其他游戏版本。

动态 sidecar 的既有零覆盖结果继续是零覆盖。静态证明不能改写为动态测试通过，也不证明错用途/过期/篡改 Grant 的完整原生负向矩阵、Pawn/Playable、多人对局或发布就绪。`native_authority_admission_verified` 保持 false；BP-038 总体验收仍未完成。
