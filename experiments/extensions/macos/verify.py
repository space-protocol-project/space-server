"""Измеряет свойства ОС; успешный CI не означает наличие защищённого macOS-хоста."""
import json
import platform
from pathlib import Path
import subprocess

results = {'platform': platform.platform(), 'machine': platform.machine(), 'samples': []}
try:
    for mode in ['as', 'data', 'aggregate']:
        process = subprocess.run(['./memory-probe', mode], capture_output=True,
                                 text=True, timeout=15, check=True)
        result = json.loads(process.stdout)
        samples = result.get('children', [result.get('sample')])
        assert all(sample['set_errno'] == 0 and sample['task_error'] == 0 for sample in samples), result
        assert all(sample['limit'] == 64 * 1024 * 1024 for sample in samples), result
        if mode == 'aggregate':
            result['both_children_allocated'] = all(sample['allocated'] == 48 * 1024 * 1024 for sample in samples)
            result['resident_sum_exceeds_one_limit'] = result['resident_sum'] > 64 * 1024 * 1024
        else:
            result['observed_exceeds_limit'] = result['sample']['resident'] > result['sample']['limit']
        results['samples'].append(result)
    process = subprocess.run(['./virtualization-probe'], capture_output=True, text=True, timeout=15, check=True)
    results['virtualization'] = json.loads(process.stdout)
    results['macos_process_host_enabled'] = False
finally:
    Path('macos-probe.json').write_text(json.dumps(results, indent=2) + '\n')
