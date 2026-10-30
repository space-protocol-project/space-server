"""Измеряет свойства ОС; успешный CI не означает наличие защищённого macOS-хоста."""
import json
import platform
from pathlib import Path
import subprocess

results = {'platform': platform.platform(), 'machine': platform.machine(), 'samples': []}
try:
    cases = [(mode, limit) for mode in ['as', 'data'] for limit in ['64', '256', '512', 'baseline']]
    cases += [('aggregate', '64'), ('aggregate', 'baseline')]
    for mode, limit in cases:
        process = subprocess.run(['./memory-probe', mode, limit], capture_output=True,
                                 text=True, timeout=15, check=True)
        result = json.loads(process.stdout)
        result['limit_mode'] = limit
        results['samples'].append(result)
        samples = result.get('children', [result.get('sample')])
        for sample in samples:
            assert sample['task_error'] == 0, sample
            sample['limit_installed'] = sample['set_errno'] == 0
            if sample['set_errno'] != 0:
                assert sample['allocated'] == 0, sample
                continue
            assert sample['limit'] == sample['requested_limit'], sample
            if sample['allocated']:
                assert sample['task_error'] == 0 and sample['resident'] > 0, sample
            else:
                assert sample['mmap_errno'] != 0, sample
        if mode == 'aggregate':
            result['both_children_allocated'] = all(sample['allocated'] == 48 * 1024 * 1024 for sample in samples)
            result['resident_sum_exceeds_64mib'] = result['resident_sum'] > 64 * 1024 * 1024
        else:
            result['observed_exceeds_limit'] = result['sample']['resident'] > result['sample']['limit']
    process = subprocess.run(['./virtualization-probe'], capture_output=True, text=True, timeout=15, check=True)
    results['virtualization'] = json.loads(process.stdout)
    results['macos_process_host_enabled'] = False
finally:
    Path('macos-probe.json').write_text(json.dumps(results, indent=2) + '\n')
