import Foundation
import Virtualization

// Только исследовательский CI launcher: без сети, диска и общих каталогов.
final class GuestDelegate: NSObject, VZVirtualMachineDelegate {
    func guestDidStop(_ virtualMachine: VZVirtualMachine) { exit(0) }
    func virtualMachine(_ virtualMachine: VZVirtualMachine, didStopWithError error: Error) {
        fputs("VM stopped: \(error)\n", stderr)
        exit(2)
    }
}

guard (CommandLine.arguments.count == 4 || CommandLine.arguments.count == 5), VZVirtualMachine.isSupported else {
    fputs("Virtualization unavailable or invalid arguments\n", stderr)
    exit(2)
}
let loader = VZLinuxBootLoader(kernelURL: URL(fileURLWithPath: CommandLine.arguments[1]))
loader.initialRamdiskURL = URL(fileURLWithPath: CommandLine.arguments[2])
loader.commandLine = "console=hvc0 rdinit=/init panic=-1 cgroup_no_v1=all"
if CommandLine.arguments.count == 5 {
    guard CommandLine.arguments[4] == "hold" else { exit(2) }
    loader.commandLine += " p1_hold=1"
}
let serial = VZVirtioConsoleDeviceSerialPortConfiguration()
serial.attachment = try VZFileSerialPortAttachment(url: URL(fileURLWithPath: CommandLine.arguments[3]), append: false)
let configuration = VZVirtualMachineConfiguration()
configuration.platform = VZGenericPlatformConfiguration()
configuration.bootLoader = loader
configuration.cpuCount = 2
configuration.memorySize = 1024 * 1024 * 1024
configuration.serialPorts = [serial]
configuration.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
try configuration.validate()
let delegate = GuestDelegate()
let vm = VZVirtualMachine(configuration: configuration)
vm.delegate = delegate
vm.start { result in
    if case .failure(let error) = result {
        fputs("VM start failed: \(error)\n", stderr)
        exit(2)
    }
}
DispatchQueue.main.asyncAfter(deadline: .now() + 240) {
    fputs("VM deadline exceeded\n", stderr)
    exit(3)
}
RunLoop.main.run()
