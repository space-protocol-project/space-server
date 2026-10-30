import Foundation
import Virtualization

// Проверка доступности API, без запуска гостевой ОС.
let result: [String: Any] = [
    "supported": VZVirtualMachine.isSupported,
    "minimum_guest_memory": VZVirtualMachineConfiguration.minimumAllowedMemorySize,
    "maximum_guest_memory": VZVirtualMachineConfiguration.maximumAllowedMemorySize,
    "os": ProcessInfo.processInfo.operatingSystemVersionString
]
let data = try JSONSerialization.data(withJSONObject: result, options: [.sortedKeys])
print(String(decoding: data, as: UTF8.self))
